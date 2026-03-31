#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/minipunch-direct-XXXXXX")"
KEEP_ARTIFACTS_ON_SUCCESS="${KEEP_ARTIFACTS_ON_SUCCESS:-0}"
PIDS=()

BASE_PORT="${BASE_PORT:-$((23000 + RANDOM % 10000))}"
SERVER_PORT="${SERVER_PORT:-$BASE_PORT}"
WEB_PORT="${WEB_PORT:-$((BASE_PORT + 1))}"
LOCAL_PORT="${LOCAL_PORT:-$((BASE_PORT + 2))}"
SRC_UDP_PORT="${SRC_UDP_PORT:-$((BASE_PORT + 3))}"
TGT_UDP_PORT="${TGT_UDP_PORT:-$((BASE_PORT + 4))}"

SERVER_URL="http://127.0.0.1:${SERVER_PORT}"
DB_PATH="${TMP_DIR}/direct.db"
SOURCE_CONFIG="${TMP_DIR}/source.toml"
TARGET_CONFIG="${TMP_DIR}/target.toml"
SOURCE_RUNTIME="${TMP_DIR}/source.runtime.json"
TARGET_RUNTIME="${TMP_DIR}/target.runtime.json"
WEB_SERVER_SCRIPT="${TMP_DIR}/web_server.py"
DIRECT_OUTPUT="${TMP_DIR}/direct.txt"
PAYLOAD_OUTPUT="${TMP_DIR}/payload.bin"

export HTTP_PROXY=
export HTTPS_PROXY=
export ALL_PROXY=
export http_proxy=
export https_proxy=
export all_proxy=

log() {
  printf '[direct-smoke] %s\n' "$*"
}

cleanup() {
  local exit_code=$?
  set +e
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  for pid in "${PIDS[@]:-}"; do
    wait "$pid" 2>/dev/null || true
  done
  if [[ "$exit_code" -eq 0 && "$KEEP_ARTIFACTS_ON_SUCCESS" != "1" ]]; then
    rm -rf "$TMP_DIR"
  else
    log "artifacts kept at ${TMP_DIR}"
  fi
}
trap cleanup EXIT

json_get() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

path, expr = sys.argv[1], sys.argv[2]
with open(path, "r", encoding="utf-8") as handle:
    data = json.load(handle)
result = eval(expr, {}, {"data": data})
if isinstance(result, bool):
    print("true" if result else "false")
else:
    print(result)
PY
}

wait_for_json_expr() {
  local file="$1"
  local expr="$2"
  local timeout_seconds="$3"
  local description="$4"
  local deadline=$((SECONDS + timeout_seconds))
  while (( SECONDS < deadline )); do
    if [[ -f "$file" ]]; then
      local value
      value="$(json_get "$file" "$expr" 2>/dev/null || true)"
      if [[ "$value" == "true" ]]; then
        return 0
      fi
    fi
    sleep 1
  done
  log "timed out waiting for ${description}"
  return 1
}

wait_for_bootstrap() {
  local attempt
  for attempt in $(seq 1 30); do
    if BOOTSTRAP_JSON="$(curl --noproxy '*' -fsS -X POST "${SERVER_URL}/api/v1/bootstrap/init" 2>/dev/null)"; then
      export BOOTSTRAP_JSON
      return 0
    fi
    sleep 1
  done
  log "failed to bootstrap server"
  return 1
}

cd "$ROOT_DIR"
cat > "$WEB_SERVER_SCRIPT" <<'PY'
from http.server import BaseHTTPRequestHandler, HTTPServer
import sys


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/payload.txt":
            body = b"z" * 262144
        else:
            body = b"minipunch direct smoke ok\n"
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        super().log_message(format, *args)


if __name__ == "__main__":
    port = int(sys.argv[1])
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
PY

log "starting minipunch server on ${SERVER_URL}"
cargo run --quiet --bin minipunch-server -- --listen-addr "127.0.0.1:${SERVER_PORT}" --database "$DB_PATH" \
  > "${TMP_DIR}/server.log" 2>&1 &
PIDS+=("$!")
wait_for_bootstrap

JOIN_TOKEN="$(python3 - <<'PY'
import json
import os

print(json.loads(os.environ["BOOTSTRAP_JSON"])["first_join_token"])
PY
)"
ADMIN_TOKEN="$(python3 - <<'PY'
import json
import os

print(json.loads(os.environ["BOOTSTRAP_JSON"])["admin_token"])
PY
)"
SECOND_JOIN_JSON="$(curl --noproxy '*' -fsS -X POST \
  -H "x-admin-token: ${ADMIN_TOKEN}" \
  -H "content-type: application/json" \
  -d '{}' \
  "${SERVER_URL}/api/v1/admin/join-tokens")"
export SECOND_JOIN_JSON
SECOND_JOIN_TOKEN="$(python3 - <<'PY'
import json
import os

print(json.loads(os.environ["SECOND_JOIN_JSON"])["join_token"])
PY
)"

log "initializing source and target agents"
cargo run --quiet --bin minipunch-agent -- --config "$SOURCE_CONFIG" init \
  --server-url "$SERVER_URL" \
  --join-token "$JOIN_TOKEN" \
  --device-name direct-source \
  > "${TMP_DIR}/source-init.log" 2>&1
cargo run --quiet --bin minipunch-agent -- --config "$TARGET_CONFIG" init \
  --server-url "$SERVER_URL" \
  --join-token "$SECOND_JOIN_TOKEN" \
  --device-name direct-target \
  > "${TMP_DIR}/target-init.log" 2>&1

SOURCE_DEVICE_ID="$(python3 - "$SOURCE_CONFIG" <<'PY'
import sys
import tomllib

with open(sys.argv[1], "rb") as handle:
    print(tomllib.load(handle)["device_id"])
PY
)"
TARGET_DEVICE_ID="$(python3 - "$TARGET_CONFIG" <<'PY'
import sys
import tomllib

with open(sys.argv[1], "rb") as handle:
    print(tomllib.load(handle)["device_id"])
PY
)"

log "starting local HTTP server on 127.0.0.1:${WEB_PORT}"
python3 "$WEB_SERVER_SCRIPT" "$WEB_PORT" \
  > "${TMP_DIR}/web.log" 2>&1 &
PIDS+=("$!")
sleep 1

log "publishing direct-enabled target service and source auto-forward rule"
cargo run --quiet --bin minipunch-agent -- --config "$TARGET_CONFIG" publish \
  --name web \
  --target-host 127.0.0.1 \
  --target-port "$WEB_PORT" \
  --allow "$SOURCE_DEVICE_ID" \
  --enable-direct \
  --udp-bind "127.0.0.1:${TGT_UDP_PORT}" \
  --candidate-type local \
  --direct-wait-seconds 5 \
  > "${TMP_DIR}/target-publish.log" 2>&1
cargo run --quiet --bin minipunch-agent -- --config "$SOURCE_CONFIG" add-forward \
  --name direct-web \
  --target-device "$TARGET_DEVICE_ID" \
  --service web \
  --local-bind "127.0.0.1:${LOCAL_PORT}" \
  --transport auto \
  --udp-bind "127.0.0.1:${SRC_UDP_PORT}" \
  --candidate-type local \
  --direct-wait-seconds 5 \
  > "${TMP_DIR}/source-forward.log" 2>&1

log "starting target and source run loops"
RUST_LOG=info cargo run --quiet --bin minipunch-agent -- --config "$TARGET_CONFIG" run \
  > "${TMP_DIR}/target-run.log" 2>&1 &
PIDS+=("$!")
RUST_LOG=info cargo run --quiet --bin minipunch-agent -- --config "$SOURCE_CONFIG" run \
  > "${TMP_DIR}/source-run.log" 2>&1 &
PIDS+=("$!")

wait_for_json_expr \
  "$SOURCE_RUNTIME" \
  'any(obs["name"] == "direct-web" and obs["active_transport"] == "direct" for obs in data["forward_observations"])' \
  40 \
  "source direct activation"
wait_for_json_expr \
  "$TARGET_RUNTIME" \
  'any(obs["name"] == "web" and obs["active_transport"] == "direct" for obs in data["published_service_observations"])' \
  40 \
  "target direct activation"

log "verifying direct traffic through local forward"
curl --noproxy '*' -fsS "http://127.0.0.1:${LOCAL_PORT}/" -o "$DIRECT_OUTPUT"
grep -q "minipunch direct smoke ok" "$DIRECT_OUTPUT"

curl --noproxy '*' -fsS "http://127.0.0.1:${LOCAL_PORT}/payload.txt" -o "$PAYLOAD_OUTPUT"
PAYLOAD_SIZE="$(wc -c < "$PAYLOAD_OUTPUT" | tr -d ' ')"
if [[ "$PAYLOAD_SIZE" != "262144" ]]; then
  log "unexpected payload size: ${PAYLOAD_SIZE}"
  exit 1
fi

wait_for_json_expr \
  "$SOURCE_RUNTIME" \
  'any(obs["name"] == "direct-web" and obs["direct_connection_count"] > 0 for obs in data["forward_observations"])' \
  20 \
  "source direct connection count"
wait_for_json_expr \
  "$TARGET_RUNTIME" \
  'any(obs["name"] == "web" and obs["direct_connection_count"] > 0 for obs in data["published_service_observations"])' \
  20 \
  "target direct connection count"

log "direct-only smoke passed"
