# Platform Python Workspace

- 把这里当作多模块服务包，而不是一次性脚本。
- 优先做最小正确根因修复，不要重写模块边界。
- 多步骤任务优先在 `reports/` 下维护 `spec.md`、`plan.md`、`progress.md`、`validation.md`。
- 先跑最窄测试理解失败，再跑 `pytest -q`。
- 如果需要总结，默认写到 `reports/change-summary.md`，除非任务要求其他报告路径。
