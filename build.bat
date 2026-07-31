@echo off
setlocal EnableDelayedExpansion

for /f "delims=" %%i in ('findstr /c:"var version" main.go') do set "LINE=%%i"
set "VER=!LINE:*"=!"
set "VER=!VER:"=!"
if not defined VER (
    echo Cannot read version from main.go
    exit /b 1
)
set "OUTPUT=iptest-web-%VER%.exe"

echo Building %OUTPUT% ...
go build -ldflags "-s -w -X main.version=%VER%" -o "%OUTPUT%" .
if errorlevel 1 (
    echo Build failed.
    exit /b 1
)
echo Build complete: %OUTPUT%
