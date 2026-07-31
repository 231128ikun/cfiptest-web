#!/bin/sh
# Read the version hard-coded in main.go; build artifacts use it directly.
set -e

VER=$(sed -n 's/^var version = "\(.*\)"$/\1/p' main.go)
if [ -z "$VER" ]; then
    echo "Cannot read version from main.go" >&2
    exit 1
fi
OUTPUT="iptest-web-$VER.exe"

echo "Building $OUTPUT ..."
go build -ldflags "-s -w -X main.version=$VER" -o "$OUTPUT" .
echo "Build complete: $OUTPUT"
