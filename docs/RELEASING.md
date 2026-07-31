# 发布流程

## 发布前条件

- 工作区只包含准备发布的源码改动。
- 上游代码、依赖和随包数据文件的许可证及署名要求已经核对并落实。
- `CHANGELOG.md` 已把“未发布”内容归入本次版本。
- GitHub Actions 和本地完整检查全部通过。

## 版本与构建

项目采用 `YYYY.MM.DD` 日历版本号；同日多次发布可使用 `YYYY.MM.DD.2`。

Windows 发布构建：

```powershell
.\build.bat 2026.07.31
```

Git Bash、Linux 或 macOS：

```bash
sh build.sh 2026.07.31
```

显式传入版本号可以保证 Git tag、页面显示版本和发布附件一致。

## 验证

```bash
go test ./...
go vet ./...
node scripts/store_test.mjs
node scripts/table_test.mjs
node scripts/export_test.mjs
node scripts/dom_test.mjs
```

另外至少执行一次实际启动冒烟测试，确认首页、`/api/config`、文本导入、任务启动/停止和导出可用。

## 发布包内容

建议发布包只包含：

- `iptest-web.exe`
- `使用说明.txt` 或 README 摘要
- 必须随包提供的许可证、NOTICE 和数据文件署名
- 可选的预下载 `locations.json` 与 `GeoLite2-ASN.mmdb`

`config.json` 可以省略，让程序首次启动生成默认配置。若随包提供，必须确认其中没有私有地址或凭据。

## Git 发布

1. 提交版本文档与代码。
2. 创建与嵌入版本一致的 annotated tag，例如 `v2026.07.31`。
3. 推送 `main` 和 tag。
4. 在仓库 Release 页面上传压缩后的发布包，并粘贴对应变更记录。
5. 下载发布附件进行一次干净环境复测。

不要把 `release/`、exe、运行数据库或本机配置直接提交到源码分支。
