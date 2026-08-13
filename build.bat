@echo off
setlocal EnableDelayedExpansion

for /f "delims=" %%i in ('findstr /c:"var version =" main.go') do set "LINE=%%i"
set "VER=!LINE:*"=!"
set "VER=!VER:"=!"
if not defined VER (
    echo Cannot read version from main.go
    exit /b 1
)
set "OUTPUT=iptest-web-%VER%.exe"
rem each version gets its own folder: release\<version>\
set "OUTDIR=release\!OUTPUT:~0,-4!"
if not exist "%OUTDIR%" mkdir "%OUTDIR%"

echo Building %OUTDIR%\%OUTPUT% ...
go build -ldflags "-s -w" -o "%OUTDIR%\%OUTPUT%" .
if errorlevel 1 (
    echo Build failed.
    exit /b 1
)
echo Build complete: %OUTDIR%\%OUTPUT%
