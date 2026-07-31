@echo off
setlocal

set "VER=%~1"
if not defined VER for /f %%i in ('git log -1 --format=%%cd --date=format:%%Y.%%m.%%d-%%H.%%M') do set "VER=%%i"
set "OUTPUT=iptest-web-%VER%.exe"

echo Building %OUTPUT% ...
go build -ldflags "-s -w -X main.version=%VER%" -o "%OUTPUT%" .
if errorlevel 1 (
    echo Build failed.
    exit /b 1
)
echo Build complete: %OUTPUT%
