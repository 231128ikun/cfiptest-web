#!/bin/sh
# Read the version hard-coded in main.go; build artifacts use it directly.
# Each version gets its own folder: release/iptest-web-<version>/
set -e

VER=$(sed -n 's/^var version = "\(.*\)"$/\1/p' main.go)
if [ -z "$VER" ]; then
    echo "Cannot read version from main.go" >&2
    exit 1
fi

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
    mingw*|msys*|cygwin*) OS=windows ;;
esac
ARCH=$(go env GOARCH)
OUTDIR="release/iptest-web-$VER"
mkdir -p "$OUTDIR"

if [ "$OS" = "windows" ]; then
    OUTPUT="iptest-web-$VER.exe"
else
    OUTPUT="iptest-web-$VER-$OS-$ARCH"
fi

echo "Building $OUTDIR/$OUTPUT ..."
go build -ldflags "-s -w -X main.version=$VER" -o "$OUTDIR/$OUTPUT" .
echo "Build complete: $OUTDIR/$OUTPUT"