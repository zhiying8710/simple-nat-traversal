#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ASSET_DIR="${REPO_ROOT}/apps/minipunch-desktop/assets"
SVG_PATH="${ASSET_DIR}/icon.svg"
PNG_PATH="${ASSET_DIR}/AppIcon.png"
ICO_PATH="${ASSET_DIR}/AppIcon.ico"
ICNS_PATH="${ASSET_DIR}/AppIcon.icns"

magick -background none "${SVG_PATH}" -resize 1024x1024 "PNG32:${PNG_PATH}"

python3 - <<'PY'
from PIL import Image
from pathlib import Path

png_path = Path("apps/minipunch-desktop/assets/AppIcon.png")
ico_path = Path("apps/minipunch-desktop/assets/AppIcon.ico")
icns_path = Path("apps/minipunch-desktop/assets/AppIcon.icns")

image = Image.open(png_path).convert("RGBA")
image.save(
    ico_path,
    format="ICO",
    sizes=[(16, 16), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)],
)
image.save(icns_path, format="ICNS")
PY

echo "generated ${PNG_PATH}"
echo "generated ${ICO_PATH}"
echo "generated ${ICNS_PATH}"
