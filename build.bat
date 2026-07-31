@echo off
REM 构建 iptest-web.exe；可传入版本号，不传时使用构建时刻。
setlocal

set "VER=%~1"
if not defined VER for /f %%i in ('powershell -NoProfile -Command "Get-Date -Format yyyy.MM.dd-HH.mm"') do set VER=%%i

echo 构建版本 %VER% ...
go build -ldflags "-s -w -X main.version=%VER%" -o iptest-web.exe .
if errorlevel 1 (
    echo 构建失败
    exit /b 1
)
echo 完成：iptest-web.exe  ^(%VER%^)
