#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "" || "${2:-}" == "" || "${3:-}" == "" || "${4:-}" == "" ]]; then
  cat <<'USAGE'
usage: scripts/package-linux.sh <version> <desktop-binary> <agent-binary> <out-tar.gz>
USAGE
  exit 1
fi

VERSION="$1"
DESKTOP_BIN="$2"
AGENT_BIN="$3"
OUT_TARBALL="$4"

PACKAGE_ROOT="minipunch-linux-${VERSION}"
TMP_DIR="$(mktemp -d)"
STAGE_DIR="${TMP_DIR}/${PACKAGE_ROOT}"
BIN_DIR="${STAGE_DIR}/bin"
APP_DIR="${STAGE_DIR}/share/applications"

cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${BIN_DIR}" "${APP_DIR}"
cp "${DESKTOP_BIN}" "${BIN_DIR}/minipunch-desktop"
cp "${AGENT_BIN}" "${BIN_DIR}/minipunch-agent"
chmod +x "${BIN_DIR}/minipunch-desktop" "${BIN_DIR}/minipunch-agent"

cat > "${APP_DIR}/minipunch-desktop.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Version=1.0
Name=MiniPunch Desktop
Comment=MiniPunch lightweight private network desktop
Exec=minipunch-desktop
Terminal=false
Categories=Network;
DESKTOP

cat > "${STAGE_DIR}/README.txt" <<README
MiniPunch Desktop ${VERSION}

Files:
- bin/minipunch-desktop
- bin/minipunch-agent
- share/applications/minipunch-desktop.desktop

Notes:
- Tray support on Linux may require libayatana-appindicator or an equivalent system tray provider.
- Autostart is installed at ~/.config/autostart/minipunch-desktop.desktop by the GUI.
README

mkdir -p "$(dirname "${OUT_TARBALL}")"
rm -f "${OUT_TARBALL}"
tar -czf "${OUT_TARBALL}" -C "${TMP_DIR}" "${PACKAGE_ROOT}"

echo "created ${OUT_TARBALL}"
