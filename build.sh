#!/bin/sh
# Cross-compile foodplace binaries into ./dist for all supported platforms.
# Requires Go. Only maintainers need this; end users install prebuilt binaries.
#
# Usage: ./build.sh [version]
set -eu

VERSION="${1:-dev}"
BIN="foodplace"
mkdir -p dist

for p in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  os=${p%/*}
  arch=${p#*/}
  name="${BIN}_${VERSION}_${os}_${arch}"
  out="dist/${name}/${BIN}"
  [ "$os" = "windows" ] && out="${out}.exe"
  echo "Building ${name}"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$out" .
  if [ "$os" = "windows" ]; then
    (cd dist && zip -qr "${name}.zip" "$name")
  else
    tar -czf "dist/${name}.tar.gz" -C dist "$name"
  fi
done

echo "Artifacts in ./dist:"
ls -1 dist
