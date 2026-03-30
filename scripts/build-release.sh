#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:-}"
if [[ -z "${VERSION}" ]]; then
  echo "usage: $0 <version>" >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${ROOT_DIR}/dist/${VERSION}"
COMMIT="$(git -C "${ROOT_DIR}" rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X simple-nat-traversal/internal/buildinfo.Version=${VERSION} -X simple-nat-traversal/internal/buildinfo.Commit=${COMMIT} -X simple-nat-traversal/internal/buildinfo.BuiltAt=${BUILT_AT}"

mkdir -p "${DIST_DIR}"

HOST_GOOS="$(go env GOOS)"
HOST_GOARCH="$(go env GOARCH)"

prepare_wails_frontend() {
  local frontend_dir="${ROOT_DIR}/internal/wailsapp/frontend"
  if ! command -v npm >/dev/null 2>&1; then
    echo "npm is required to build the Wails frontend" >&2
    exit 1
  fi
  echo "preparing Wails frontend"
  (
    cd "${frontend_dir}"
    npm ci
    npm run build
  )
}

build_one() {
  local goos="$1"
  local goarch="$2"
  local pkg="$3"
  local name="$4"
  local ext=""
  if [[ "${goos}" == "windows" ]]; then
    ext=".exe"
  fi
  local out="${DIST_DIR}/${name}-${VERSION}-${goos}-${goarch}${ext}"
  echo "building ${out}"
  GOOS="${goos}" GOARCH="${goarch}" go build -ldflags "${LDFLAGS}" -o "${out}" "${pkg}"
}

build_gui_if_native_host() {
  local goos="$1"
  local goarch="$2"
  local pkg="$3"
  local name="$4"
  if [[ "${HOST_GOOS}" != "${goos}" || "${HOST_GOARCH}" != "${goarch}" ]]; then
    echo "skipping ${name}-${VERSION}-${goos}-${goarch}: native ${goos}/${goarch} build host required for desktop GUI packaging (current host ${HOST_GOOS}/${HOST_GOARCH})"
    return
  fi
  build_one "${goos}" "${goarch}" "${pkg}" "${name}"
}

build_macos_gui_arch() {
  local goarch="$1"
  local out="$2"
  local clang_arch=""
  case "${goarch}" in
    amd64) clang_arch="x86_64" ;;
    arm64) clang_arch="arm64" ;;
    *)
      echo "unsupported macOS arch: ${goarch}" >&2
      exit 1
      ;;
  esac

  local sdkroot
  sdkroot="$(xcrun --sdk macosx --show-sdk-path)"
  env \
    SDKROOT="${sdkroot}" \
    CGO_ENABLED=1 \
    GOOS=darwin \
    GOARCH="${goarch}" \
    CC="clang -arch ${clang_arch}" \
    CXX="clang++ -arch ${clang_arch}" \
    CGO_CFLAGS="-arch ${clang_arch}" \
    CGO_CXXFLAGS="-arch ${clang_arch}" \
    CGO_LDFLAGS="-arch ${clang_arch} -framework UniformTypeIdentifiers -mmacosx-version-min=10.13" \
    go build -tags production -ldflags "${LDFLAGS}" -o "${out}" ./cmd/snt-gui
}

build_macos_universal() {
  local tmp_dir
  tmp_dir="$(mktemp -d)"
  local cli_arm64="${tmp_dir}/snt-arm64"
  local cli_amd64="${tmp_dir}/snt-amd64"
  local gui_arm64="${tmp_dir}/snt-gui-arm64"
  local gui_amd64="${tmp_dir}/snt-gui-amd64"
  local cli_out="${DIST_DIR}/snt-${VERSION}-darwin-universal"
  local gui_out="${DIST_DIR}/snt-gui-${VERSION}-darwin-universal"
  local dmg_out="${DIST_DIR}/client-macos-universal.dmg"

  trap 'rm -rf "${tmp_dir}"' RETURN

  echo "building ${cli_out}"
  GOOS=darwin GOARCH=arm64 go build -ldflags "${LDFLAGS}" -o "${cli_arm64}" ./cmd/snt
  GOOS=darwin GOARCH=amd64 go build -ldflags "${LDFLAGS}" -o "${cli_amd64}" ./cmd/snt

  echo "building ${gui_out}"
  build_macos_gui_arch arm64 "${gui_arm64}"
  build_macos_gui_arch amd64 "${gui_amd64}"

  lipo -create -output "${cli_out}" "${cli_arm64}" "${cli_amd64}"
  lipo -create -output "${gui_out}" "${gui_arm64}" "${gui_amd64}"

  chmod +x "${ROOT_DIR}/scripts/package-macos.sh"
  "${ROOT_DIR}/scripts/package-macos.sh" "${VERSION}" "universal" "${gui_out}" "${cli_out}" "${dmg_out}"
}

build_one linux amd64 ./cmd/snt-server snt-server
build_one linux amd64 ./cmd/snt snt

if [[ "${HOST_GOOS}" == "darwin" ]]; then
  prepare_wails_frontend
  build_macos_universal
else
  echo "skipping macOS DMG packaging: run this script on macOS to build desktop packages"
fi

if [[ "${HOST_GOOS}" == "windows" ]]; then
  echo "windows installer packaging is handled by scripts/build-release.ps1 on a Windows host"
fi

echo "release artifacts written to ${DIST_DIR}"
