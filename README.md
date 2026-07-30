# iptest-web：Cloudflare IP 测速工具（本地 Web 版）

在命令行工具 [iptest](https://github.com/Kwisma/iptest) 的基础上改造的可交互 Web 应用：
Go 后端 + 内嵌网页，编译为单个 exe，**双击即用，自动打开浏览器**。

![工作流程](工作流程：输入整理 → 延迟测试 → 结果筛选 → 测速 → 导出)

## 功能

- **输入整理**：粘贴任意格式 IP 列表（`ip:port` / 纯 IP / 空格分隔 / IPv6 / 中文冒号 / 带注释 / CSV 元数据），一键规范化、去重、按表达式筛选（`port:443,8443 country:JP`，空格=且、`|`=或）
- **延迟测试**：并发 TCP 拨测 + 复用连接请求 `/cdn-cgi/trace` 验证真实 Cloudflare 节点，自动识别数据中心、地理位置（中文+国旗）、出站 IP、ASN、TLS/WARP 等全部 trace 字段
- **下载测速**：对勾选或筛选后的结果子集测速（也可全部测），支持速度阈值过滤
- **结果整理**：表格按延迟/速度/国家/数据中心排序，关键词过滤，**国家配额选择**（如日本取 5 个、美国取 10 个）
- **导出**：格式模板自定义（默认 `{ip}:{port}#{emoji}{country}`），追加式结果框可手改、再去重；下载 TXT / CSV（全部 trace 字段可逐列选择，带 BOM，Excel 无乱码），一键复制

## 使用

```bash
# 直接运行（Windows）
iptest-web.exe

# 指定端口 / 不自动开浏览器
iptest-web.exe -port 18080 -no-browser
```

首次启动自动下载 `locations.json`（地理位置）与 `GeoLite2-ASN.mmdb`（ASN 数据库）到 exe 同目录，
并生成配置文件 `config.json`（见下节）。

浏览器中按四步走：

1. **输入整理** —— 粘贴 IP →「整理」→（可选）表达式筛选
2. **测试配置与执行** —— 设置并发/超时/延迟过滤 →「开始延迟测试」，实时看进度与结果
3. **结果** —— 排序/过滤/配额选择，勾选后可「对勾选结果测速」
4. **结果框与导出** ——「追加到结果框」→ 复制 / 下载 TXT / 下载 CSV

## 配置文件

首次启动在 exe 同目录生成 `config.json`，可手改后重启生效：

```json
{
  "sources": {
    "locations":      ["https://locations-adw.pages.dev/", "https://speed.cloudflare.com/locations"],
    "asnDatabase":    ["https://jsd.onmicrosoft.cn/gh/seketiti/GeoLiet2@release/GeoLite2-ASN.mmdb",
                       "https://cdn.jsdelivr.net/gh/P3TERX/GeoLite.mmdb@download/GeoLite2-ASN.mmdb"],
    "officialRanges": ["https://api.cloudflare.com/client/v4/ips"]
  },
  "speedTestURL": "speed.cloudflare.com/__down?bytes=500000000",
  "traceURL":     "speed.cloudflare.com/cdn-cgi/trace"
}
```

三条规则：

- **数组依次 fallback** —— 第一个源失败自动试下一个，全部失败才报错。想换源就把自己的地址放在数组第一位。
- **本地文件优先** —— 同目录已存在 `locations.json` / `GeoLite2-ASN.mmdb` 时**永不下载**。
  所以「自备数据文件」只需把文件放进去即可；反之，**想强制更新就删掉对应文件再重启**。
- **缺字段回落默认值** —— 配置文件写一半也能启动，未写的字段用内置默认值。

## 提前停止（最大结果数）

延迟测试与下载测速都支持「凑够 N 个就停」：

- 留空或填 **0 = 不限制**，全部目标测完；
- 填 N（> 0）则凑够 N 个合格结果即中止剩余任务，可大幅节省时间。

注意得到的是**最先找到的 N 个**，不是全部测完后最优的 N 个。
`done` 事件会区分三种结束原因：`completed`（测完）/ `limit`（达到上限）/ `stopped`（用户停止），
后两者都属正常收工，不会弹错误提示。

## 官方优选 IP（CIDR 网段）

支持直接输入 CIDR 网段（可与普通 IP 混写），按 [CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest)
的惯例抽样：**同一 /24 内的 IP 几乎总走同一 Cloudflare 机房，故每 /24 取 1 个即可代表**。
官方全部 15 个 IPv4 网段共 1,524,736 个地址，按此抽样后为 **5,956 个**，才是可实测的规模。

抽样模式（`sampleMode`）：`one`（默认，每 /24 取 1 个）/ `n`（每 /24 取 `sampleN` 个）/ `all`（全取，百万级，不建议）。

网段的展开与抽样只在后端实现一份（`internal/engine/cidr.go`，有单测），
所以含 `/` 的文本一律走 `POST /api/import/text`，前端不重写抽样逻辑。
一次解析最多返回 20 万个目标，超出会返回 413 并提示改用更粗的抽样粒度——
官方段「全取」是 152 万个地址，序列化成 JSON 堆进浏览器会直接把页面卡死。

## 导入远程列表

粘贴框之外还可以直接填一个 TXT 地址，由后端代抓（绝大多数订阅源不给
`Access-Control-Allow-Origin`，浏览器 fetch 会被 CORS 拦掉，Go 取则没这个限制）。

限制：只放行 `http` / `https`（`file://` 会让这个端点变成任意本地文件读取器）、
响应体 8 MB、20 秒超时、重定向最多 5 跳。

**解析到内网或保留地址的目标会被拒绝**（回环 / 私有段 / 链路本地 / 组播 /
CGNAT / 云厂商 metadata 端点如 `169.254.169.254`）。校验发生在 TCP 拨号那一刻而非
事前 DNS 查询，因此重定向的每一跳、以及「先解析到公网、真连接时再解析到 127.0.0.1」
的 DNS rebinding 都拦得住。代价是内网自建的 IP 列表也导不进来，此时请改用粘贴或本地文件。

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
  config/               config.json 读写、多源 fallback、缺字段回落默认值
  engine/               测试引擎（纯逻辑，可独立测试）
    trace.go            TCP 拨测 + 连接劫持 + trace 验证（核心）
    speed.go            下载测速
    runner.go           worker pool 编排、ctx 取消、提前停止、流式事件回调
    parser.go           IP 文本多格式解析 + 去重（含 CIDR 混写）
    cidr.go             CIDR 展开与抽样（每 /24 取代表）
    official_ranges.go  官方 IP 段默认源与内置兜底数据
    locations.go/asn.go 地理位置与 ASN 数据库加载
  server/               HTTP API + SSE 事件推送（单任务模型）
    handler_import.go   IP 列表导入（粘贴文本 / 代抓远程 TXT，含 CIDR 展开与内网拦截）
    handler_ranges.go   官方 IP 段接口 + 各抽样模式预估数量
web/                    前端（原生 JS 模块，无框架）
  js/store.js           状态层（工作区/候选区/结果；筛选只产生派生视图）
  js/input.js           输入整理（规范化/去重/筛选 DSL）
  js/columns.js         列注册表（表格列与 CSV 列的唯一来源）
  js/table.js           结果表格（排序/过滤/配额/勾选）
  js/composer.js        输出格式模板引擎
  js/exporter.js        TXT/CSV 导出（serialize 与投递解耦）
  js/app.js             流水线主控
scripts/                前端模块的 node 校验脚本（store_test.mjs / table_test.mjs）
```

与旧 CLI 版相比修复了：测速协程共享信号量导致的死锁（`-speedtest > -max` 时）、
进度计数与结果切片的数据竞争；测试全程支持取消（停止按钮）。

## HTTP API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/config` | 版本号、默认参数与资源加载状态 |
| GET | `/api/official-ranges` | 官方 IP 段 + 各抽样模式的预估数量 + 可用端口（`?n=` 指定每 /24 取几个）|
| POST | `/api/import/text` | `{text, sampleMode, sampleN}` 解析任意 IP 文本并展开其中的 CIDR，返回规范化目标 |
| POST | `/api/import/remote` | `{url, sampleMode, sampleN}` 代抓远程 IP 列表（绕开浏览器 CORS）后同上解析 |
| POST | `/api/task/latency` | `{targets:[{ip,port}]} 或 {rawText}, sampleMode, sampleN, options` 启动延迟测试 |
| POST | `/api/task/speed` | `{targets, options}` 启动测速 |
| POST | `/api/task/stop` | `{taskId}` 停止当前任务 |
| GET | `/api/task/events` | SSE 事件流（`result`/`progress`/`speed`/`done`/`error`；`done` 带 `reason`）|

参数均可缺省，后端回落到默认值（与 CLI 版默认值一致）。
`options.maxResults` 显式传 `0` 表示不限制，与「不传」等价。

## 致谢

核心测试逻辑源自 [Kwisma/iptest](https://github.com/Kwisma/iptest)（基于 XIU2 的源码修改）；
输入整理与筛选 DSL 移植自 DDNS-cf-proxyip 项目前端。
