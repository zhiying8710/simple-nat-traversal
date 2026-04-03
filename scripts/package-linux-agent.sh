#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "" || "${2:-}" == "" || "${3:-}" == "" ]]; then
  cat <<'USAGE'
usage: scripts/package-linux-agent.sh <version> <agent-binary> <out-tar.gz>
USAGE
  exit 1
fi

VERSION="$1"
AGENT_BIN="$2"
OUT_TARBALL="$3"

PACKAGE_ROOT="minipunch-agent-linux-${VERSION}"
TMP_DIR="$(mktemp -d)"
STAGE_DIR="${TMP_DIR}/${PACKAGE_ROOT}"
BIN_DIR="${STAGE_DIR}/bin"

cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${BIN_DIR}"
cp "${AGENT_BIN}" "${BIN_DIR}/minipunch-agent"
chmod +x "${BIN_DIR}/minipunch-agent"

cat > "${STAGE_DIR}/README.txt" <<README
MiniPunch Agent ${VERSION}

Files:
- bin/minipunch-agent

Quick start:
1. Copy bin/minipunch-agent to a directory in PATH, for example /usr/local/bin
2. Join the network on this device:

   minipunch-agent init \\
     --server-url http://<server>:9443 \\
     --join-token <join-token> \\
     --device-name <device-name>

3. Publish a local service or add a forward rule, then run:

   minipunch-agent run

Notes:
- This package contains only the Linux CLI agent binary.
- It is suitable for headless Linux clients or testing without the desktop GUI.
README

mkdir -p "$(dirname "${OUT_TARBALL}")"
rm -f "${OUT_TARBALL}"
tar -czf "${OUT_TARBALL}" -C "${TMP_DIR}" "${PACKAGE_ROOT}"

echo "created ${OUT_TARBALL}"
