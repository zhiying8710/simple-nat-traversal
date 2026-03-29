#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: $0 <version> <arch> <gui-binary> <cli-binary> <output-dmg>" >&2
  exit 1
fi

VERSION="$1"
ARCH="$2"
GUI_BIN="$3"
CLI_BIN="$4"
OUTPUT_DMG="$5"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGE_DIR="$(mktemp -d)"
APP_NAME="Simple NAT Traversal.app"
APP_DIR="${STAGE_DIR}/${APP_NAME}"
CONTENTS_DIR="${APP_DIR}/Contents"
MACOS_DIR="${CONTENTS_DIR}/MacOS"
RESOURCES_DIR="${CONTENTS_DIR}/Resources"

cleanup() {
  rm -rf "${STAGE_DIR}"
}
trap cleanup EXIT

mkdir -p "${MACOS_DIR}" "${RESOURCES_DIR}"

cp "${GUI_BIN}" "${MACOS_DIR}/snt-gui"
chmod +x "${MACOS_DIR}/snt-gui"

cat > "${CONTENTS_DIR}/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key>
  <string>en</string>
  <key>CFBundleDisplayName</key>
  <string>Simple NAT Traversal</string>
  <key>CFBundleExecutable</key>
  <string>snt-gui</string>
  <key>CFBundleIdentifier</key>
  <string>io.github.zhiying8710.simple-nat-traversal</string>
  <key>CFBundleInfoDictionaryVersion</key>
  <string>6.0</string>
  <key>CFBundleName</key>
  <string>Simple NAT Traversal</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>${VERSION}</string>
  <key>CFBundleVersion</key>
  <string>${VERSION}</string>
  <key>LSMinimumSystemVersion</key>
  <string>11.0</string>
  <key>NSHighResolutionCapable</key>
  <true/>
</dict>
</plist>
EOF

cp "${CLI_BIN}" "${STAGE_DIR}/snt"
chmod +x "${STAGE_DIR}/snt"
cp "${ROOT_DIR}/README.md" "${STAGE_DIR}/README.md"
cp "${ROOT_DIR}/docs/DEPLOYMENT.md" "${STAGE_DIR}/DEPLOYMENT.md"
cp "${ROOT_DIR}/docs/USER_GUIDE.md" "${STAGE_DIR}/USER_GUIDE.md"
cp "${ROOT_DIR}/docs/GUI_CLIENT.md" "${STAGE_DIR}/GUI_CLIENT.md"
cp "${ROOT_DIR}/examples/client-macos.json" "${STAGE_DIR}/client.example.json"

mkdir -p "$(dirname "${OUTPUT_DMG}")"
rm -f "${OUTPUT_DMG}"
hdiutil create \
  -volname "Simple NAT Traversal ${ARCH}" \
  -srcfolder "${STAGE_DIR}" \
  -ov \
  -format UDZO \
  "${OUTPUT_DMG}"
