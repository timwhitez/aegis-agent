# 单题执行与评分

批调度入口是同目录的 `scheduler.py`。每个 attempt 使用冻结的题目、配置、Aegis Linux 二进制和证据工具。调度器与单题运行器共用 `task_plan.read_plan()`。运行环境要求 Linux、Python 3.12 或更新版本、`harbor==0.26.0`、可用的 Docker Compose 和可写的 `/dev/shm`。

运行器支持本次数据集使用的单步 Linux 任务。Agent 与 verifier 各自使用 TOML 对应节声明的镜像、CPU、内存和超时。运行器要求预构建镜像和显式 agent 超时，对缺失资源、未支持的任务拓扑以及要求传入宿主环境变量的 Compose 配置在运行模型之前报错。任务包、预构建镜像和评分脚本必须来自操作者认可的来源。

Harbor 原生处理服务的启动、工作目录、网络、评分和声明的文件交接。独立 verifier 在新容器中读取产物，目录排除规则与服务来源沿用题目定义。共享环境的评分在 agent 返回后注入测试，清除既有 reward，再执行正式评分。共享模式会保留题目要求的后台服务，不能提供独立容器级别的防篡改保证。Agent 超时且不能确认进程结束时跳过共享评分。

适配器将 Harbor 提供的完整题面作为 `exec … --` 后的单个参数，保留内嵌和末尾换行。Harbor 先按原生规则去掉题面头部的数据出处标记。配置只引用容器内的当次代理连接，正式 provider 凭据留在宿主。Agent 返回后撤销代理连接。

Harbor 的任务日志、原始报告和评分交接目录位于每次尝试独有的临时内存文件系统中。最终目录保存经宿主证据工具再次校验的 `diagnostics.json`、环境观测和 `outcome.json`。环境观测中的 `image_id` 来自实际运行的 Docker 容器，CPU 以十亿分之一核为单位，内存以字节为单位。任务计划中的镜像引用和资源值来自输入文件。退出码与正式 reward 各自保留。只有 `status=completed` 且具有有限数值 reward 的结果可进入常规评分统计。

对完整题目目录执行 `python validation/terminalbench/task_plan.py PATH_TO_TASKS`，可以离线将调度计划与原生 Harbor 解析的两阶段镜像、CPU、内存、超时和评分模式逐项核对。该命令不启动容器或调用模型。

在具备上述依赖、已有 `python:3.12-slim` 镜像的 Linux 开发机执行以下验证。命令会创建一个独立的本地评分镜像，结束时自动清理。合成 agent 无需模型调用。

~~~sh
CGO_ENABLED=0 go build -o /tmp/aegis-evidence ./validation/cmd/benchevidence
python validation/terminalbench/smoke_docker.py --evidence-helper /tmp/aegis-evidence
~~~

验证覆盖共享环境的后台服务、新 reward、独立 verifier 的文件传递、目录排除、Compose 辅助服务、连字符多行题面，取消后的诊断与产物、退出状态不一致时阻止共享评分，以及只有脱敏数据进入结果目录。Harbor 的安装和任务格式可查阅 [0.26.0 发布页](https://github.com/harbor-framework/harbor/releases/tag/v0.26.0)。
