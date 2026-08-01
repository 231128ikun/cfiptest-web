#!/bin/sh
# Read the version hard-coded in main.go; build artifacts use it directly.
# Each version gets its own folder: release/<version>/
set -e

VER=$(sed -n 's/^var version = "\(.*\)"$/\1/p' main.go)
if [ -z "$VER" ]; then
    echo "Cannot read version from main.go" >&2
    exit 1
fi
OUTPUT="iptest-web-$VER.exe"
OUTDIR="release/${OUTPUT%.exe}"
mkdir -p "$OUTDIR"

echo "Building $OUTDIR/$OUTPUT ..."
go build -ldflags "-s -w -X main.version=$VER" -o "$OUTDIR/$OUTPUT" .
echo "Build complete: $OUTDIR/$OUTPUT"
