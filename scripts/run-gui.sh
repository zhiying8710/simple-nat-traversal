#!/usr/bin/env bash
set -euo pipefail

CONFIG_PATH="${1:-./client.json}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -x "${SCRIPT_DIR}/snt-gui" ]]; then
  GUI_BIN="${SCRIPT_DIR}/snt-gui"
elif [[ -x "${SCRIPT_DIR}/../snt-gui" ]]; then
  GUI_BIN="${SCRIPT_DIR}/../snt-gui"
else
  echo "snt-gui not found next to launcher or in parent directory" >&2
  exit 1
fi

"${GUI_BIN}" -config "${CONFIG_PATH}"
