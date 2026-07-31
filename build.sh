#!/bin/sh
# 构建 iptest-web.exe；可传入版本号，不传时使用构建时刻。
set -e

VER=${1:-$(date +%Y.%m.%d-%H.%M)}

echo "构建版本 $VER ..."
go build -ldflags "-s -w -X main.version=$VER" -o iptest-web.exe .
echo "完成：iptest-web.exe ($VER)"
