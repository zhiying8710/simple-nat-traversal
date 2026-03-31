#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/minipunch-handoff-XXXXXX")"
KEEP_ARTIFACTS_ON_SUCCESS="${KEEP_ARTIFACTS_ON_SUCCESS:-0}"
PIDS=()

BASE_PORT="${BASE_PORT:-$((20000 + RANDOM % 10000))}"
SERVER_PORT="${SERVER_PORT:-$BASE_PORT}"
WEB_PORT="${WEB_PORT:-$((BASE_PORT + 1))}"
LOCAL_PORT="${LOCAL_PORT:-$((BASE_PORT + 2))}"
SRC_UDP_PORT="${SRC_UDP_PORT:-$((BASE_PORT + 3))}"
TGT_UDP_PORT="${TGT_UDP_PORT:-$((BASE_PORT + 4))}"

SERVER_URL="http://127.0.0.1:${SERVER_PORT}"
DB_PATH="${TMP_DIR}/smoke.db"
SOURCE_CONFIG="${TMP_DIR}/source.toml"
TARGET_CONFIG="${TMP_DIR}/target.toml"
SOURCE_RUNTIME="${TMP_DIR}/source.runtime.json"
TARGET_RUNTIME="${TMP_DIR}/target.runtime.json"
WEB_SERVER_SCRIPT="${TMP_DIR}/web_server.py"
SLOW_OUTPUT="${TMP_DIR}/slow.bin"
FALLBACK_OUTPUT="${TMP_DIR}/fallback.html"
DIRECT_OUTPUT="${TMP_DIR}/direct.html"

export HTTP_PROXY=
export HTTPS_PROXY=
export ALL_PROXY=
export http_proxy=
export https_proxy=
export all_proxy=

log() {
  printf '[smoke] %s\n' "$*"
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

run_cargo() {
  cargo run --quiet --bin "$@"
}

cd "$ROOT_DIR"
cat > "$WEB_SERVER_SCRIPT" <<'PY'
from http.server import BaseHTTPRequestHandler, HTTPServer
import sys
import time


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/slow":
            chunk = b"x" * 4096
            total_chunks = 80
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(len(chunk) * total_chunks))
            self.end_headers()
            for _ in range(total_chunks):
                self.wfile.write(chunk)
                self.wfile.flush()
                time.sleep(0.5)
            return

        body = b"minipunch handoff smoke ok\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
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
  --device-name smoke-source \
  > "${TMP_DIR}/source-init.log" 2>&1
cargo run --quiet --bin minipunch-agent -- --config "$TARGET_CONFIG" init \
  --server-url "$SERVER_URL" \
  --join-token "$SECOND_JOIN_TOKEN" \
  --device-name smoke-target \
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

log "publishing relay-only target service and source auto-forward rule"
cargo run --quiet --bin minipunch-agent -- --config "$TARGET_CONFIG" publish \
  --name web \
  --target-host 127.0.0.1 \
  --target-port "$WEB_PORT" \
  --allow "$SOURCE_DEVICE_ID" \
  > "${TMP_DIR}/target-publish.log" 2>&1
cargo run --quiet --bin minipunch-agent -- --config "$SOURCE_CONFIG" add-forward \
  --name handoff-web \
  --target-device "$TARGET_DEVICE_ID" \
  --service web \
  --local-bind "127.0.0.1:${LOCAL_PORT}" \
  --transport auto \
  --udp-bind "127.0.0.1:${SRC_UDP_PORT}" \
  --candidate-type local \
  --direct-wait-seconds 8 \
  > "${TMP_DIR}/source-forward.log" 2>&1

log "starting target relay runtime and source auto runtime"
RUST_LOG=info cargo run --quiet --bin minipunch-agent -- --config "$TARGET_CONFIG" run \
  > "${TMP_DIR}/target-run.log" 2>&1 &
PIDS+=("$!")
MINIPUNCH_TEST_FAIL_DIRECT_PREBRIDGE_ONCE=1 RUST_LOG=info cargo run --quiet --bin minipunch-agent -- --config "$SOURCE_CONFIG" run \
  > "${TMP_DIR}/source-run.log" 2>&1 &
PIDS+=("$!")

wait_for_json_expr \
  "$SOURCE_RUNTIME" \
  'any(obs["name"] == "handoff-web" and obs["state"] == "relay_active" for obs in data["forward_observations"])' \
  30 \
  "source relay fallback activation"

log "opening a slow relay connection to keep relay draining"
curl --noproxy '*' -fsS "http://127.0.0.1:${LOCAL_PORT}/slow" -o "$SLOW_OUTPUT" \
  > "${TMP_DIR}/slow-curl.log" 2>&1 &
SLOW_CURL_PID="$!"
PIDS+=("$SLOW_CURL_PID")

wait_for_json_expr \
  "$SOURCE_RUNTIME" \
  'any(obs["name"] == "handoff-web" and obs["active_connection_count"] > 0 for obs in data["forward_observations"])' \
  30 \
  "active relay drain connection"

log "starting target direct responder on 127.0.0.1:${TGT_UDP_PORT}"
cargo run --quiet --bin minipunch-agent -- --config "$TARGET_CONFIG" direct-tcp-serve \
  --service web \
  --udp-bind "127.0.0.1:${TGT_UDP_PORT}" \
  --candidate-type local \
  --wait-seconds 12 \
  > "${TMP_DIR}/target-direct.log" 2>&1 &
PIDS+=("$!")

wait_for_json_expr \
  "$SOURCE_RUNTIME" \
  'any(obs["name"] == "handoff-web" and obs["active_transport"] == "direct" for obs in data["forward_observations"])' \
  45 \
  "source direct handoff activation"

log "opening a new connection during relay drain to trigger direct handoff fallback"
curl --noproxy '*' -fsS "http://127.0.0.1:${LOCAL_PORT}/" -o "$FALLBACK_OUTPUT"

wait_for_json_expr \
  "$SOURCE_RUNTIME" \
  'any("state=direct_handoff_fallback" in event["message"] for event in data["recent_events"])' \
  20 \
  "direct_handoff_fallback runtime event"

log "allowing relay drain to finish"
kill "$SLOW_CURL_PID" 2>/dev/null || true
wait "$SLOW_CURL_PID" 2>/dev/null || true

wait_for_json_expr \
  "$SOURCE_RUNTIME" \
  'any("relay drain finished; direct is now the sole ingress" in event["message"] for event in data["recent_events"])' \
  30 \
  "relay drain completion event"

log "verifying direct remains healthy after relay drain"
curl --noproxy '*' -fsS "http://127.0.0.1:${LOCAL_PORT}/" -o "$DIRECT_OUTPUT"
wait_for_json_expr \
  "$SOURCE_RUNTIME" \
  'any(obs["name"] == "handoff-web" and obs["active_transport"] == "direct" and obs["state"] == "direct_active" for obs in data["forward_observations"])' \
  20 \
  "steady-state direct ingress"

log "smoke succeeded"
python3 - "$SOURCE_RUNTIME" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    data = json.load(handle)

interesting = [
    event["message"]
    for event in data["recent_events"]
    if "direct_handoff_fallback" in event["message"]
    or "relay drain finished; direct is now the sole ingress" in event["message"]
]
for message in interesting:
    print(f"[smoke] {message}")
PY
