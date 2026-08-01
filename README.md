# iptest-web：Cloudflare IP 测速工具（本地 Web 版）

在命令行工具 [iptest](https://github.com/Kwisma/iptest) 的基础上改造的可交互 Web 应用：
Go 后端 + 内嵌网页，编译为单个 exe，**双击即用，自动打开浏览器**。

工作流程：输入整理 → 延迟测试 → 结果筛选 → 测速 → 导出。

## 功能

- **准备候选**：反代模式支持本地或远程 TXT/CSV、粘贴输入；保留/排除会直接在原输入框显示结果，重置后恢复原文。CSV 会识别中英文 IP、端口、国家、城市列，并转换为 `ip:port#国家-城市`。
- **输入筛选**：支持 `port:443` 精确端口、`port:8000-9000` 范围、`country:日本`、`-port:9443` / `country!=美国` 排除条件，以及 `|` / `||` 或组合。
- **检测规则**：只测延迟时使用延迟并发；勾选继续测速时，使用两者较小并发逐 IP 执行“延迟 → 测速 → 最终判断”。检测参数与资源地址可保存到本地。
- **检测及结果**：支持动态字段、筛选和排序；显示字段支持全选、恢复默认并保存到 `data/settings.json`；“自定义展示规则”可按国家、端口、ASN、数据中心等字段多选叠加，按选择顺序分组输出，0 或留空表示不限制。
- **直接导出**：导出格式支持 TXT / CSV，范围为全部、当前规则或自定义；自定义默认沿用当前规则结果，也可清空后手动追加勾选结果。TXT 默认模板为 `{ip}:{port}#{country}`，支持自定义并保存到本地；CSV 直接复用结果表选择的字段。
- **结果颜色**：延迟与速度徽章的快/中/慢阈值可在“其他检测规则与本地配置”中调整，保存后写入 `data/settings.json`。

## 使用

```bash
# 直接运行（Windows，文件名中的时间为构建时间）
iptest-web-2026.07.31-14.30.exe

# 指定端口 / 不自动开浏览器
iptest-web-2026.07.31-14.30.exe -port 18080 -no-browser
```

发布时只需分发一个 EXE。首次启动会在 EXE 同目录自动创建 `data` 文件夹；地理位置、ASN 数据库、官方 IP 缓存、应用配置和检测设置都集中存放在该目录。首次联网会自动下载并缓存位置与 ASN 数据；离线时程序仍可启动，但相关字段会暂时留空。

浏览器中按三步走：

1. **准备候选** —— 选择反代或官方模式，导入并筛选候选。
2. **检测规则** —— 设置延迟、速度与整体数量约束。
3. **检测及结果** —— 开始/停止任务，筛选与勾选结果，直接复制或导出 TXT / CSV。

纯 IP 未指定端口时不会在导入阶段擅自补值；开始执行时按 TLS 规则补全：TLS 开启为 443，关闭为 80。显式端口始终保持不变。

## 本地配置

程序首次启动会创建 `data/config.json`，也可在网页第二步的“其他检测规则与本地配置”中修改并保存：

```json
{
  "sources": {
    "locations":      ["https://locations-adw.pages.dev/", "https://speed.cloudflare.com/locations"],
    "asnDatabase":    ["https://jsd.onmicrosoft.cn/gh/seketiti/GeoLiet2@release/GeoLite2-ASN.mmdb",
                       "https://cdn.jsdelivr.net/gh/P3TERX/GeoLite.mmdb@download/GeoLite2-ASN.mmdb"],
    "officialRanges": ["https://api.cloudflare.com/client/v4/ips"]
  },
  "speedTestURL": "speed.cloudflare.com/__down?bytes=500000000",
  "traceURL":     "speed.cloudflare.com/cdn-cgi/trace",
  "ipsTypeURL":   "https://api.ipapi.is/?q={ip}"
}
```

三条规则：

- **数组依次 fallback** —— 第一个源失败自动试下一个，全部失败才报错。想换源就把自己的地址放在数组第一位。
- **本地文件优先** —— `data` 目录已存在 `locations.json` / `GeoLite2-ASN.mmdb` 时**永不下载**。
  所以「自备数据文件」只需把文件放进去即可；反之，**想强制更新就删掉对应文件再重启**。
- **缺字段回落默认值** —— 本地配置缺少字段或数组为空时，后端使用当前内置默认值。
- **官方网段独立缓存** —— 配置只定义远程源；IPv4/IPv6 内容分别缓存为两个 TXT 文件，页面可显式更新。
- **检测设置持久化** —— `data/settings.json` 保存延迟/测速规则、显示字段、自定义展示规则、导出格式/范围与自定义模板。
- **IPS 地址占位符** —— `ipsTypeURL` 中的 `{ip}` 会替换为当前被测 IP。

## 满足条件后自动停止

整体约束中的数量表示「测试过程中凑够 N 个最终合格结果就停」：

- 默认留空，留空或填 **0 = 不限制**，全部目标测完；
- 填 N（> 0）则凑够 N 个合格结果即中止剩余任务，可大幅节省时间。

只测延迟时按延迟条件合格数计算；自动继续测速时按同时满足延迟和速度条件的结果数计算。

注意得到的是**最先找到的 N 个**，不是全部测完后最优的 N 个。用户手动停止时，
只测延迟会保留已完成的延迟结果；自动测速模式只保留已经完整完成延迟和测速的结果。
`done` 事件会区分三种结束原因：`completed`（测完）/ `limit`（达到上限）/ `stopped`（用户停止），
后两者都属正常收工，不会弹错误提示。

## 延迟检测后继续测速

速度条件始终可以编辑，并用于第三步的补充测速。标题行的复选框只决定初次检测
是否自动执行“延迟 → 速度”流水线：未勾选时按延迟并发完成延迟检测；勾选后并发数
取延迟并发与测速并发的较小值，每个工作单元逐 IP 执行“延迟 → 测速 → 最终判断”。
只有同时满足延迟与速度条件的节点才进入结果表，统一最大数量按最终结果计数。

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

# 发布构建：显式指定时间版本号
build.bat 2026.07.31-14.30

# 或（Git Bash / Linux / macOS）
sh build.sh
sh build.sh 2026.07.31-14.30
```

版本号格式为 `yyyy.MM.dd-HH.mm`。构建脚本会将它注入程序，并在 `release/` 下按版本建独立文件夹输出：
`release/iptest-web-2026.07.31-14.30/iptest-web-2026.07.31-14.30.exe`；前端右上角显示 `版本号：2026.07.31-14.30`。
直接执行 `go build` 且不注入时，版本号仍为 `dev`。

依赖仅 [geoip2-golang](https://github.com/oschwald/geoip2-golang)，前端为原生 ES Modules（无构建步骤），经 `embed.FS` 打进二进制。

## 验证

提交前运行：

```bash
go test ./...
go vet ./...
node scripts/store_test.mjs
node scripts/table_test.mjs
node scripts/export_test.mjs
node scripts/dom_test.mjs
```

GitHub Actions 会在每次推送和拉取请求时执行同一组检查。

版本变化记录见 [CHANGELOG.md](CHANGELOG.md)。

## 架构

```
main.go                 入口：端口参数、版本号、资源初始化、自动开浏览器、优雅退出
build.bat / build.sh    构建脚本（注入时间戳版本号）
internal/
  config/               data/config.json 与 data/settings.json 读写
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
  js/table.js           结果表格（排序/过滤/自定义展示规则/勾选）
  js/composer.js        输出格式模板引擎
  js/exporter.js        TXT/CSV 导出（serialize 与投递解耦）
  js/app.js             流水线主控
scripts/                前端模块的 Node 校验脚本（状态、表格、导出与 DOM 接线）
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

> 公开发布前必须核对并保留上述上游项目的许可证与署名要求；在完成核对前，
> 本仓库不应自行声明一个可能与上游不兼容的许可证。

## 自动化维护（任务 → IP 库 → 运行记录）

应用按生产工具结构组织为五个页签（左侧导航）：**测速工作台 / 自动维护 / IP 库 / 运行记录 / 设置**。

- **测速工作台**：三步流水线（准备候选 → 检测规则 → 检测及结果）。第 1 步可「从本地库导入」候选；结果页可「导入当前展示 / 导入勾选」到指定 IP 库。
- **IP 库**（`data/ipdb/`）：支持多个命名库（新建 / 改名 / 删除 / 清空），库内容表格支持搜索筛选、粘贴导入、移除勾选。首次启动自动把旧版 `data/ipdb.jsonl` 迁移为「默认库」。
- **自动维护**（`data/tasks.json`）：任务卡片（Master-Detail）。每个任务绑定一个 IP 库，可设：输入订阅文件（自动解析 `ip:port#备注` 导入库）、输出文件与模板（复用导出页内置/我的模板）、总数限制、测速总开关，以及多条规则。
  - 规则：多字段（国家/城市/端口），字段内多值=或、多字段=交集（笛卡尔积），每个组合取前 N 条；延迟范围；速度范围（测速开关开启后可用）。
  - 每个任务有独立「启用」开关；顶部「一键维护」依次执行所有已启用任务。
- **运行记录**（`data/runs.jsonl`）：每次维护运行的摘要（输出行数、移除失效、缺口等），可展开查看明细，可追溯。

维护流程：**库（现测现更）→ 检测：延迟失败移除、测速失败保留、结果回写 → 按规则补足配额 → 模板输出（未设输出文件时直接覆盖更新输入文件）**。

自动化 API：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/PUT | `/api/auto/tasks` | 读取 / 全量保存维护任务 |
| POST | `/api/auto/tasks/validate` | 校验单个任务 |
| GET | `/api/auto/libraries` | IP 库列表（含统计） |
| POST | `/api/auto/libraries` | 新建 IP 库（`name`） |
| POST | `/api/auto/libraries/rename` | 改名（`id`,`name`） |
| POST | `/api/auto/libraries/delete` | 删除库（默认库不可删） |
| POST | `/api/auto/libraries/clear` | 清空库（需 `confirm:true`） |
| GET | `/api/auto/library?lib=` | 库内容（`status`/`country`/`q`/分页） |
| POST | `/api/auto/library/import` | 导入（`lib` + `targets`/`text`/`results`） |
| POST | `/api/auto/library/remove` | 按 `keys` 移除条目 |
| POST | `/api/auto/run` | 运行任务（`taskId`），进度走 SSE `auto` 事件 |
| GET | `/api/auto/runs` | 运行历史（最新在前） |
| GET | `/api/auto/output?path=` | 下载输出文件 |