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

build_one linux amd64 ./cmd/snt-server snt-server
build_one darwin arm64 ./cmd/snt snt
build_one windows amd64 ./cmd/snt snt
build_gui_if_native_host darwin arm64 ./cmd/snt-gui snt-gui
build_gui_if_native_host windows amd64 ./cmd/snt-gui snt-gui

echo "release artifacts written to ${DIST_DIR}"
