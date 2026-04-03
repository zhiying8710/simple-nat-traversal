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
TEMP_DMG="${TMP_DIR}/minipunch-desktop.dmg"
HDIUTIL_LOG="${TMP_DIR}/hdiutil-create.log"
APP_DIR="${STAGE_DIR}/${APP_BUNDLE}"
CONTENTS_DIR="${APP_DIR}/Contents"
MACOS_DIR="${CONTENTS_DIR}/MacOS"
RESOURCES_DIR="${CONTENTS_DIR}/Resources"
BIN_DIR="${RESOURCES_DIR}/bin"
DMG_VOLUME_NAME="${APP_NAME} ${VERSION}"

cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

detach_stale_mounts() {
  if ! command -v hdiutil >/dev/null 2>&1; then
    return 0
  fi

  local volume_path="/Volumes/${DMG_VOLUME_NAME}"
  local detached=0
  while IFS= read -r device; do
    [[ -n "${device}" ]] || continue
    if hdiutil detach "${device}" -force >/dev/null 2>&1; then
      detached=1
    fi
  done < <(hdiutil info | awk -v volume_path="${volume_path}" '
    $1 ~ /^\/dev\// && index($0, volume_path) { print $1 }
  ' | sort -u)

  if [[ "${detached}" -eq 1 ]]; then
    sleep 1
  fi
}

create_dmg_with_retry() {
  local max_attempts=4
  local attempt

  for attempt in $(seq 1 "${max_attempts}"); do
    rm -f "${TEMP_DMG}" "${HDIUTIL_LOG}"
    if hdiutil create \
      -volname "${DMG_VOLUME_NAME}" \
      -srcfolder "${STAGE_DIR}" \
      -ov \
      -format UDZO \
      "${TEMP_DMG}" > /dev/null 2> "${HDIUTIL_LOG}"; then
      mv "${TEMP_DMG}" "${OUT_DMG}"
      return 0
    fi

    if grep -q "Resource busy" "${HDIUTIL_LOG}"; then
      echo "hdiutil create reported 'Resource busy' on attempt ${attempt}/${max_attempts}; retrying..." >&2
      detach_stale_mounts
      sleep $((attempt * 2))
      continue
    fi

    cat "${HDIUTIL_LOG}" >&2
    return 1
  done

  cat "${HDIUTIL_LOG}" >&2
  return 1
}

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
detach_stale_mounts

create_dmg_with_retry

echo "created ${OUT_DMG}"
