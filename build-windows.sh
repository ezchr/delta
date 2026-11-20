#!/bin/sh
# Builds the native core and the plugin wheel for Windows (win_amd64). Run from Git Bash
# with Go and a MinGW-w64 gcc on PATH (cgo needs it for -buildmode=c-shared).
#   ./build-windows.sh -> dist/endstone_delta-<version>-py3-none-win_amd64.whl
set -e
cd "$(dirname "$0")"
VERSION=${VERSION:-0.1.0.dev0}
mkdir -p build dist
(cd core && CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build $GOFLAGS_EXTRA -buildmode=c-shared -buildvcs=false -trimpath \
	-ldflags "-s -w -X main.version=$VERSION" -o ../build/delta-windows-amd64.dll ./cmd/libdelta)
rm -f build/*.h
python tools/mkwheel.py --version "$VERSION" --plat win_amd64 --native build/delta-windows-amd64.dll --out dist
