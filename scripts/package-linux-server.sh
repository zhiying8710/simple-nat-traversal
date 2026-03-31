#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "" || "${2:-}" == "" || "${3:-}" == "" ]]; then
  cat <<'USAGE'
usage: scripts/package-linux-server.sh <version> <server-binary> <out-tar.gz>
USAGE
  exit 1
fi

VERSION="$1"
SERVER_BIN="$2"
OUT_TARBALL="$3"

PACKAGE_ROOT="minipunch-server-linux-${VERSION}"
TMP_DIR="$(mktemp -d)"
STAGE_DIR="${TMP_DIR}/${PACKAGE_ROOT}"
BIN_DIR="${STAGE_DIR}/bin"
SYSTEMD_DIR="${STAGE_DIR}/systemd"

cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${BIN_DIR}" "${SYSTEMD_DIR}"
cp "${SERVER_BIN}" "${BIN_DIR}/minipunch-server"
chmod +x "${BIN_DIR}/minipunch-server"

cat > "${SYSTEMD_DIR}/minipunch-server.service" <<'UNIT'
[Unit]
Description=MiniPunch Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/minipunch-server
ExecStart=/opt/minipunch-server/bin/minipunch-server --listen-addr 0.0.0.0:9443 --database /var/lib/minipunch/minipunch.db
Restart=on-failure
RestartSec=3
User=minipunch
Group=minipunch

[Install]
WantedBy=multi-user.target
UNIT

cat > "${STAGE_DIR}/README.txt" <<README
MiniPunch Server ${VERSION}

Files:
- bin/minipunch-server
- systemd/minipunch-server.service

Quick start:
1. Copy bin/minipunch-server to /opt/minipunch-server/bin/minipunch-server
2. Create a writable database directory, for example /var/lib/minipunch
3. Optionally install systemd/minipunch-server.service
4. Start the server:

   /opt/minipunch-server/bin/minipunch-server \\
     --listen-addr 0.0.0.0:9443 \\
     --database /var/lib/minipunch/minipunch.db

Notes:
- This package contains only the Linux control-plane server binary.
- TLS termination, reverse proxying and firewalling should be handled by your VPS deployment.
README

mkdir -p "$(dirname "${OUT_TARBALL}")"
rm -f "${OUT_TARBALL}"
tar -czf "${OUT_TARBALL}" -C "${TMP_DIR}" "${PACKAGE_ROOT}"

echo "created ${OUT_TARBALL}"
