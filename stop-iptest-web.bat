@echo off
chcp 65001 >nul
echo 正在停止所有 iptest-web 进程...
powershell -NoProfile -Command "Get-Process -Name 'iptest-web*' -ErrorAction SilentlyContinue | Stop-Process -Force"
echo 已完成：所有 iptest-web 实例已关闭。
echo 如需使用请重新双击 exe 启动。
pause
