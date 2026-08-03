# AGENTS.md

## 交付构建约定（每次代码修改后必须执行）

1. 修改 `main.go` 中的版本号 `var version`，改为当前时间，格式 `yyyy.MM.dd-HH.mm`（例如 `2026.08.02-00.10`）。
2. 运行 `.\build.bat`（Windows）或 `./build.sh`（Linux/macOS）构建：
   - 产物输出到 `release\<版本号>\iptest-web-<版本号>.exe`（每个版本一个独立文件夹）。
3. 构建成功后，启动该 exe 验证可访问（默认 `http://127.0.0.1:18080/`）。
4. 纯文档类改动（如本文件）不影响产物，无需重新构建。
