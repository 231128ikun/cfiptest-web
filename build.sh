#!/bin/sh
# 构建 iptest-web.exe，版本号 = 构建时刻（2026.07.29-01.21）
set -e

VER=$(date +%Y.%m.%d-%H.%M)

echo "构建版本 $VER ..."
go build -ldflags "-s -w -X main.version=$VER" -o iptest-web.exe .
echo "完成：iptest-web.exe ($VER)"
