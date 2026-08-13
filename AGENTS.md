# AGENTS.md

## 代码结构

- `main.go`：唯一入口，只解析命令行参数并转发给共享启动逻辑。
- `internal/app/`：平台无关的服务启动、端口管理、生命周期。
- `internal/platform/`：操作系统相关适配（DNS、浏览器、进程），通过文件名后缀与 build tag 区分。
- `internal/{config,engine,server,cloud,library,netutil,subscription}`：业务核心。
- `android/`：Android WebView 外壳（独立 Gradle 工程）。
- `web/`：共享前端资源。

## 交付构建约定（每次代码修改后必须执行）

1. 修改 `main.go` 中的版本号 `var version`，改为当前时间，格式 `yyyy.MM.dd-HH.mm`（例如 `2026.08.02-00.10`）。
2. 按需构建对应平台产物：
   - Windows：运行 `.\build.bat`，产物输出到 `release\iptest-web-<版本号>\iptest-web-<版本号>.exe`。
   - Linux / macOS：运行 `./build.sh`，产物输出到 `release\iptest-web-<版本号>\iptest-web-<版本号>-<os>-<arch>`。
   - Android：按 `android/README.md` 构建 APK，产物放入 `release\iptest-web-<版本号>\`。
3. 构建成功后，启动对应产物验证可访问（默认 `http://127.0.0.1:18080/`）。
4. 纯文档类改动不影响产物，无需重新构建。