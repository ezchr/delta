#!/bin/sh
# Builds the native core and the plugin wheel (linux-amd64).
#   ./build.sh                 -> dist/endstone_javabridge-<version>-py3-none-linux_x86_64.whl
# Environment: VERSION (PEP 440, default 0.1.0.dev0), GOFLAGS_EXTRA, BUILD_WRAP (a command
# prefix such as "flock /tmp/build.lock nice -n 19" on a shared machine).
set -e
cd "$(dirname "$0")"
VERSION=${VERSION:-0.1.0.dev0}
mkdir -p build dist
(cd core && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 $BUILD_WRAP go build $GOFLAGS_EXTRA -buildmode=c-shared -buildvcs=false -trimpath \
	-ldflags "-s -w -X main.version=$VERSION" -o ../build/libjavabridge-linux-amd64.so ./cmd/libjavabridge)
rm -f build/*.h
python3 tools/mkwheel.py --version "$VERSION" --plat linux_x86_64 --native build/libjavabridge-linux-amd64.so --out dist
