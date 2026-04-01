# Go Patch Workspace

- 目标是做最小正确修复，不要重写整个模块。
- 先运行现有测试理解失败，再改代码。
- 如果需要输出说明，写到 `reports/change-summary.md`。
- 验证优先使用已有 `go test ./...`。
