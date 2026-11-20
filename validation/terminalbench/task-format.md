# 单题执行与评分

批调度入口是同目录的 `scheduler.py`。每个 attempt 使用冻结的题目、配置、Aegis Linux 二进制和证据工具。调度器与单题运行器共用 `task_plan.read_plan()`。运行环境要求 Linux、Python 3.12 或更新版本、`harbor==0.26.0`、可用的 Docker Compose 和可写的 `/dev/shm`。

运行器支持本次数据集使用的单步 Linux 任务。Agent 与 verifier 各自使用 TOML 对应节声明的镜像、CPU、内存和超时。运行器要求预构建镜像和显式 agent 超时，对缺失资源、未支持的任务拓扑以及要求传入宿主环境变量的 Compose 配置在运行模型之前报错。任务包、预构建镜像和评分脚本必须来自操作者认可的来源。

Harbor 按题目配置启动服务、设置工作目录和网络、交接文件并评分。Agent 使用题目指定的 Unix 用户。独立 verifier 在新容器中读取产物，目录排除规则与服务来源沿用题目定义。正式评分开始前，运行器清除上阶段的 reward、CTRF 和测试输出。如果评分阶段没有开始，结果标为 `not_started`。共享模式在 agent 返回后注入测试，并保留题目要求的后台服务。Agent 超时且无法确认进程结束时，运行器跳过共享评分。共享模式的进程和文件仍在同一容器内。

适配器将 Harbor 提供的完整题面作为 `exec … --` 后的单个参数，保留内嵌和末尾换行。Harbor 先按原生规则去掉题面头部的数据出处标记。配置只引用容器内的当次代理连接，正式 provider 凭据留在宿主。Agent 返回或本次运行中止时，运行器撤销代理连接。`outcome.json` 的 `provider_http` 记录撤销前代理向上游发起的请求数和收到的响应数。代理启动前就失败的尝试没有这项观测。上游响应声明的长度不足，或分块响应缺少结束标记时，客户端会收到传输中断，可按配置重试。

Harbor 的任务日志、原始报告和评分交接目录位于每次尝试独有的临时内存文件系统中。最终目录保存经宿主证据工具再次校验的 `diagnostics.json`、环境观测和 `outcome.json`。环境观测中的 `image_id` 来自实际运行的 Docker 容器，CPU 以十亿分之一核为单位，内存以字节为单位。任务计划中的镜像引用和资源值来自输入文件。`outcome.json` 的 `agent_rc` 和 `verify_rc` 分别记录 agent 包装程序与原生评分命令的退出码。若命令没有返回，其值为 `null`。Harbor 仍按评分文件读取 reward。资源观测接受当前阶段的超时和取消信号。只有 `status=completed` 且具有有限数值 reward 的结果可进入常规评分统计。

交付物收据的 `missing` 表示结果包中缺少该交付物。Harbor 报告采集失败时同时记录 `harbor_status=failed`，仅凭该状态无法区分源路径不存在和传输失败。如果 Harbor 没有留下采集清单，收据标为 `unavailable`。清单损坏或过大时分别标为 `invalid` 和 `limit`。

`diagnostics.json` 通过 `request_id` 关联请求准备和终止事件。`requests[].status` 记录请求所处阶段，旧版报告可能省略该字段。如果本地预检拒绝请求（`rejected`），或者请求中止（`cancelled`、`failed`），本次请求的 `stop_reason` 留空。只有已记录的响应才有 provider 的停止原因。主请求在本地被拒绝或取消时，失败会话分别记为 `request_rejected`、`request_cancelled`，奖励不计入常规评分。

诊断文件上限为 4 MiB。多会话的明细合计超限时，工具优先保留根会话，并从子会话中裁去较早的请求和尝试，保留会话状态和实际进程退出码。此时 `partial=true`，报告中的请求和尝试条数只覆盖保留的明细。

对完整题目目录执行 `python validation/terminalbench/task_plan.py PATH_TO_TASKS`，可以离线将调度计划与原生 Harbor 解析的两阶段镜像、CPU、内存、超时和评分模式逐项核对。该命令不启动容器或调用模型。

在具备上述依赖、已有 `python:3.12-slim` 镜像的 Linux 开发机执行以下验证。命令会创建一个独立的本地评分镜像，结束时自动清理。合成 agent 无需模型调用。

~~~sh
CGO_ENABLED=0 go build -o /tmp/aegis-evidence ./validation/cmd/benchevidence
python validation/terminalbench/smoke_docker.py --evidence-helper /tmp/aegis-evidence
~~~

验证覆盖共享环境的后台服务、新 reward、独立 verifier 的文件传递、目录排除、Compose 辅助服务、连字符多行题面，取消后的诊断与产物、退出状态不一致时阻止共享评分，以及只有脱敏数据进入结果目录。Harbor 的安装和任务格式可查阅 [0.26.0 发布页](https://github.com/harbor-framework/harbor/releases/tag/v0.26.0)。
