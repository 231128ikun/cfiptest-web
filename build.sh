#!/bin/sh
# Build with a timestamp version unless an explicit version is provided.
set -e

VER=${1:-$(git log -1 --format='%cd' --date=format:'%Y.%m.%d-%H.%M')}
OUTPUT="iptest-web-$VER.exe"

echo "构建版本 $VER ..."
go build -ldflags "-s -w -X main.version=$VER" -o "$OUTPUT" .
echo "完成：$OUTPUT"
