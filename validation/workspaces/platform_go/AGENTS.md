# Platform Go Workspace

- 把这里当作中等规模服务，而不是单文件练习。
- 优先做最小正确根因修复，但当测试证明需要时允许跨 package 改动。
- 多步骤任务优先在 `reports/` 下维护 `spec.md`、`plan.md`、`progress.md`、`validation.md`。
- 先跑最窄测试理解失败，再跑 `go test ./...`。
- 如果需要总结，默认写到 `reports/change-summary.md`，除非任务要求其他报告路径。
