#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "" || "${2:-}" == "" || "${3:-}" == "" || "${4:-}" == "" || "${5:-}" == "" ]]; then
  cat <<'USAGE'
usage: scripts/package-macos.sh <version> <arch-label> <desktop-binary> <agent-binary> <out-dmg>

example:
  scripts/package-macos.sh 0.1.0 universal target/release/minipunch-desktop target/release/minipunch-agent dist/minipunch-macos.dmg
USAGE
  exit 1
fi

VERSION="$1"
ARCH_LABEL="$2"
DESKTOP_BIN="$3"
AGENT_BIN="$4"
OUT_DMG="$5"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ICON_ICNS="${REPO_ROOT}/apps/minipunch-desktop/assets/AppIcon.icns"

APP_NAME="MiniPunch Desktop"
APP_BUNDLE="${APP_NAME}.app"
EXECUTABLE_NAME="MiniPunch Desktop"
ICON_NAME="AppIcon.icns"
ICON_FILE_STEM="AppIcon"
TMP_DIR="$(mktemp -d)"
STAGE_DIR="${TMP_DIR}/stage"
APP_DIR="${STAGE_DIR}/${APP_BUNDLE}"
CONTENTS_DIR="${APP_DIR}/Contents"
MACOS_DIR="${CONTENTS_DIR}/MacOS"
RESOURCES_DIR="${CONTENTS_DIR}/Resources"
BIN_DIR="${RESOURCES_DIR}/bin"

cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${MACOS_DIR}" "${BIN_DIR}"

cp "${DESKTOP_BIN}" "${MACOS_DIR}/${EXECUTABLE_NAME}"
chmod +x "${MACOS_DIR}/${EXECUTABLE_NAME}"
cp "${AGENT_BIN}" "${BIN_DIR}/minipunch-agent"
chmod +x "${BIN_DIR}/minipunch-agent"
if [[ -f "${ICON_ICNS}" ]]; then
  cp "${ICON_ICNS}" "${RESOURCES_DIR}/${ICON_NAME}"
fi

xattr -cr "${APP_DIR}" >/dev/null 2>&1 || true

cat > "${CONTENTS_DIR}/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>${APP_NAME}</string>
  <key>CFBundleDisplayName</key>
  <string>${APP_NAME}</string>
  <key>CFBundleIdentifier</key>
  <string>dev.minipunch.desktop</string>
  <key>CFBundleVersion</key>
  <string>${VERSION}</string>
  <key>CFBundleShortVersionString</key>
  <string>${VERSION}</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleExecutable</key>
  <string>${EXECUTABLE_NAME}</string>
  <key>CFBundleIconFile</key>
  <string>${ICON_FILE_STEM}</string>
  <key>LSMinimumSystemVersion</key>
  <string>12.0</string>
</dict>
</plist>
PLIST

cat > "${STAGE_DIR}/README.txt" <<README
MiniPunch Desktop ${VERSION} (${ARCH_LABEL})

Contents:
- ${APP_BUNDLE}: desktop GUI with tray/autostart support
- embedded minipunch-agent CLI: ${APP_BUNDLE}/Contents/Resources/bin/minipunch-agent

Notes:
- This build is ad-hoc signed locally, but it is not notarized.
- On first launch, macOS may still require right-click -> Open or manual quarantine removal.
- Autostart uses ~/Library/LaunchAgents/${APP_NAME// /-}.plist at runtime.
README

if command -v codesign >/dev/null 2>&1; then
  codesign --force --sign - --timestamp=none "${BIN_DIR}/minipunch-agent"
  codesign --force --sign - --timestamp=none "${MACOS_DIR}/${EXECUTABLE_NAME}"
  codesign --force --deep --sign - --timestamp=none "${APP_DIR}"
  codesign --verify --deep --strict --verbose=2 "${APP_DIR}" >/dev/null
fi

ln -s /Applications "${STAGE_DIR}/Applications"

mkdir -p "$(dirname "${OUT_DMG}")"
rm -f "${OUT_DMG}"

hdiutil create \
  -volname "${APP_NAME} ${VERSION}" \
  -srcfolder "${STAGE_DIR}" \
  -ov \
  -format UDZO \
  "${OUT_DMG}" >/dev/null

echo "created ${OUT_DMG}"
