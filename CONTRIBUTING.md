# 贡献与维护指南

## 开发环境

- Go 版本以 `go.mod` 为准。
- Node.js 仅用于运行原生 ES Module 测试，不需要安装 npm 依赖。
- `config.json`、`locations.json` 和 `GeoLite2-ASN.mmdb` 是本机运行数据，不提交到 Git。

## 开发流程

1. 从 `main` 创建短期功能或修复分支。
2. 修改代码时同步更新相关测试和用户文档。
3. 提交前运行完整检查：

   ```bash
   go test ./...
   go vet ./...
   node scripts/store_test.mjs
   node scripts/table_test.mjs
   node scripts/export_test.mjs
   node scripts/dom_test.mjs
   ```

4. 确认 `git diff --check` 没有空白错误，且没有提交构建产物、运行数据或临时文件。
5. 使用简洁、可追溯的提交信息；一个提交只表达一项完整变更。

## 代码约定

- Go 代码保持 `gofmt` 格式，并优先为可独立验证的逻辑增加表驱动测试。
- 前端保持原生 ES Modules，不在没有明确收益时引入构建工具或运行时依赖。
- 表格与 CSV 字段以 `web/js/columns.js` 为唯一注册源。
- CIDR 展开与抽样只在 Go 引擎实现，前端不要复制算法。
- 远程导入相关修改必须保留 SSRF、重定向、响应大小和超时保护。

## 文档与发布

- 用户可见行为变化应更新 `README.md`。
- 每项准备发布的变化应记录到 `CHANGELOG.md` 的“未发布”小节。
- 发布包和 exe 不进入源码历史；按 `docs/RELEASING.md` 创建发布附件。
- 在公开分发前，必须确认所有上游代码和数据文件的许可证与署名要求。
