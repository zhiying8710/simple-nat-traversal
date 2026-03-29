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
    echo "skipping ${name}-${VERSION}-${goos}-${goarch}: native ${goos}/${goarch} build host required for Fyne GUI (current host ${HOST_GOOS}/${HOST_GOARCH})"
    return
  fi
  build_one "${goos}" "${goarch}" "${pkg}" "${name}"
}

build_macos_gui() {
  local goarch="$1"
  local cli_out="${DIST_DIR}/snt-${VERSION}-darwin-${goarch}"
  local gui_out="${DIST_DIR}/snt-gui-${VERSION}-darwin-${goarch}"
  local dmg_out="${DIST_DIR}/client-macos-${goarch}.dmg"

  echo "building ${cli_out}"
  GOOS=darwin GOARCH="${goarch}" go build -ldflags "${LDFLAGS}" -o "${cli_out}" ./cmd/snt

  echo "building ${gui_out}"
  if [[ "${goarch}" == "amd64" ]]; then
    local sdkroot
    sdkroot="$(xcrun --sdk macosx --show-sdk-path)"
    env \
      SDKROOT="${sdkroot}" \
      CGO_ENABLED=1 \
      GOOS=darwin \
      GOARCH=amd64 \
      CC="clang -arch x86_64" \
      CXX="clang++ -arch x86_64" \
      CGO_CFLAGS="-arch x86_64" \
      CGO_CXXFLAGS="-arch x86_64" \
      CGO_LDFLAGS="-arch x86_64" \
      go build -ldflags "${LDFLAGS}" -o "${gui_out}" ./cmd/snt-gui
  else
    env CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags "${LDFLAGS}" -o "${gui_out}" ./cmd/snt-gui
  fi

  chmod +x "${ROOT_DIR}/scripts/package-macos.sh"
  "${ROOT_DIR}/scripts/package-macos.sh" "${VERSION}" "${goarch}" "${gui_out}" "${cli_out}" "${dmg_out}"
}

build_one linux amd64 ./cmd/snt-server snt-server
build_one linux amd64 ./cmd/snt snt

if [[ "${HOST_GOOS}" == "darwin" ]]; then
  build_macos_gui arm64
  build_macos_gui amd64
else
  echo "skipping macOS DMG packaging: run this script on macOS to build desktop packages"
fi

if [[ "${HOST_GOOS}" == "windows" ]]; then
  echo "windows installer packaging is handled by scripts/build-release.ps1 on a Windows host"
fi

echo "release artifacts written to ${DIST_DIR}"
