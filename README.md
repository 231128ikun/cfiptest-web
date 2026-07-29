# iptest-web：Cloudflare IP 测速工具（本地 Web 版）

在命令行工具 [iptest](https://github.com/Kwisma/iptest) 的基础上改造的可交互 Web 应用：
Go 后端 + 内嵌网页，编译为单个 exe，**双击即用，自动打开浏览器**。

![工作流程](工作流程：输入整理 → 延迟测试 → 结果筛选 → 测速 → 导出)

## 功能

- **输入整理**：粘贴任意格式 IP 列表（`ip:port` / 纯 IP / 空格分隔 / IPv6 / 中文冒号 / 带注释 / CSV 元数据），一键规范化、去重、按表达式筛选（`port:443,8443 country:JP`，空格=且、`|`=或）
- **延迟测试**：并发 TCP 拨测 + 复用连接请求 `/cdn-cgi/trace` 验证真实 Cloudflare 节点，自动识别数据中心、地理位置（中文+国旗）、出站 IP、ASN、TLS/WARP 等全部 trace 字段
- **下载测速**：对勾选或筛选后的结果子集测速（也可全部测），支持速度阈值过滤
- **结果整理**：表格按延迟/速度/国家/数据中心排序，关键词过滤，**国家配额选择**（如日本取 5 个、美国取 10 个）
- **导出**：格式模板自定义（默认 `{ip}:{port}#{emoji}{country}`），追加式结果框可手改、再去重；下载 TXT / CSV（27 列可选，带 BOM，Excel 无乱码），一键复制

## 使用

```bash
# 直接运行（Windows）
iptest-web.exe

# 指定端口 / 不自动开浏览器
iptest-web.exe -port 18080 -no-browser
```

首次启动自动下载 `locations.json`（地理位置）与 `GeoLite2-ASN.mmdb`（ASN 数据库）到 exe 同目录。

浏览器中按四步走：

1. **输入整理** —— 粘贴 IP →「整理」→（可选）表达式筛选
2. **测试配置与执行** —— 设置并发/超时/延迟过滤 →「开始延迟测试」，实时看进度与结果
3. **结果** —— 排序/过滤/配额选择，勾选后可「对勾选结果测速」
4. **结果框与导出** ——「追加到结果框」→ 复制 / 下载 TXT / 下载 CSV

## 构建

```bash
# 推荐：自动以构建时刻为版本号（Windows）
build.bat

# 或（Git Bash / Linux / macOS）
sh build.sh
```

版本号形如 `2026.07.29-11.28`（`年.月.日-时.分`），构建时通过
`-ldflags "-X main.version=..."` 注入，由 `/api/config` 下发、显示在页面标题右侧，
用于对照「当前运行的是哪次构建」。直接 `go build` 不注入时显示 `dev`。

依赖仅 [geoip2-golang](https://github.com/oschwald/geoip2-golang)，前端为原生 ES Modules（无构建步骤），经 `embed.FS` 打进二进制。

## 架构

```
main.go                 入口：端口参数、版本号、资源初始化、自动开浏览器、优雅退出
build.bat / build.sh    构建脚本（注入时间戳版本号）
internal/
  engine/               测试引擎（纯逻辑，可独立测试）
    trace.go            TCP 拨测 + 连接劫持 + trace 验证（核心）
    speed.go            下载测速
    runner.go           worker pool 编排、ctx 取消、流式事件回调
    parser.go           IP 文本多格式解析 + 去重
    locations.go/asn.go 地理位置与 ASN 数据库加载
  server/               HTTP API + SSE 事件推送（单任务模型）
web/                    前端（原生 JS 模块，无框架）
  js/input.js           输入整理（规范化/去重/筛选 DSL）
  js/table.js           结果表格（排序/过滤/配额/勾选）
  js/composer.js        输出格式模板引擎
  js/exporter.js        TXT/CSV 导出
  js/app.js             流水线主控
```

与旧 CLI 版相比修复了：测速协程共享信号量导致的死锁（`-speedtest > -max` 时）、
进度计数与结果切片的数据竞争；测试全程支持取消（停止按钮）。

## HTTP API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/config` | 版本号、默认参数与资源加载状态 |
| POST | `/api/task/latency` | `{targets:[{ip,port}]} 或 {rawText}, options` 启动延迟测试 |
| POST | `/api/task/speed` | `{targets, options}` 启动测速 |
| POST | `/api/task/stop` | `{taskId}` 停止当前任务 |
| GET | `/api/task/events` | SSE 事件流（`result`/`progress`/`speed`/`done`/`error`） |

参数均可缺省，后端回落到默认值（与 CLI 版默认值一致）。

## 致谢

核心测试逻辑源自 [Kwisma/iptest](https://github.com/Kwisma/iptest)（基于 XIU2 的源码修改）；
输入整理与筛选 DSL 移植自 DDNS-cf-proxyip 项目前端。
