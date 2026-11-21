# 冻结、运行和恢复评测批次

在安装了评测适配器依赖的 POSIX 主机上，用拥有该批次目录、可以通过本机已有 Docker 入口运行容器的普通用户执行命令。从仓库根目录开始。调度器需要 Python 3.11 或更新版本。准备任务目录、目标平台的 Aegis 二进制、证据采集程序和评测配置。配置中通过 `api_key_env` 指定环境变量名。运行阶段从宿主环境读取其值。

```sh
python3 validation/terminalbench/scheduler.py init \
  --tasks "$TASKS" --run "$RUN" --binary "$AEGIS_BINARY" \
  --evidence-helper "$EVIDENCE_HELPER" --config "$BENCH_CONFIG"
python3 validation/terminalbench/scheduler.py run --run "$RUN" --jobs 4
python3 validation/terminalbench/scheduler.py summary --run "$RUN"
```

`init` 需要一个尚未存在的批次目录。小样本可在此步用 `--only task-a task-b` 固定题目。初始化会复制任务、运行脚本、二进制和脱敏配置，记录运行计划、文件 SHA-256、可执行位和目录清单。任务树每批复制一次。只使用来源可信、已审查的任务包与二进制。各题的容器定义和 verifier 脚本会实际执行，冻结只保证批次使用同一版内容。冻结时会拒绝指向题目目录外的符号链接。再次运行该批时，入口先校验摘要，再使用批次内的运行脚本。另开批次即可使用新版脚本或配置。

`run` 默认串行执行。`--jobs` 控制并发上限。`--cpu-budget` 和 `--memory-mb` 可限制并发任务申请的 CPU 核数和内存 MiB，按每题 agent 和 verifier 两阶段中较高的声明值预留。额外 Compose 服务的实际开销需要单独预留。若单题申请超出指定预算，调度器在启动 trial 前报告该题。

仓库入口先核对冻结运行脚本目录的文件，再启动其中的 Python 程序。冻结调度器随后校验完整资产。每阶段的 `memory_mb` 使用整数 MiB，CPU 核数可以是小数。

每题每次尝试使用 `attempts/<任务名>/<UUID>/`。目录内的 `state.json` 记录批次、尝试标识、退出状态、原因、耗时和评分，`outcome.json` 保存适配器的结果。适配器必须正常返回、状态为 `completed` 并提供有效有限数值，调度器才接纳评分。0 和小数评分都会按原值保留。超时或恢复清理等未采纳尝试若已写下原始数值，另存为 `observed_reward`。显式的 `reward_valid` 必须为布尔真才可计分，早期未写此标记的结果继续按原格式读取。缺失结果、无效基础设施、超时和中断均在下一次 `run` 时重试。成功完成的 0 分和 1 分均可跳过。第二个针对同一批次的执行进程会收到 `run already active`。

共享环境的判分脚本若明确因 pytest 启动器缺失而写下 0，尝试记为 `verifier_error`，下次运行会重试。此判断同时核对冻结脚本的报错行、测试命令及末尾的 0/1 写分逻辑，并要求没有 CTRF、用例或 pytest 已启动的记录。原始 0 保存在 `outcome.json` 和 `observed_reward`，不计入有效评分。普通断言失败、被测程序缺失，以及缺少上述证据的历史尝试沿用原判。

收到 SIGINT 或 SIGTERM 后，调度器停止接单，记录取消来源并通知已启动的适配器退出。默认每题硬时限为 agent 声明时限加 verifier 声明时限再加 1800 秒，可用 `--hard-timeout` 覆盖。到达硬时限后停止接单并取消本批其他活跃尝试，尚未开始的题保留为缺失。调度器异常退出时，独立监控进程从关闭的管道识别中断，按批次 UUID、任务名和尝试 UUID 派生的标识清理资源。清理目标从批次清单和目录计算，尝试结果中的字段不能指定另一个批次的容器。更换 Python 环境后恢复时，会同时核对已记录的适配器进程号、进程组和整组冻结参数。如果监控进程意外退出，仍在运行的调度器会停止接单，在退出前清理遗留的适配器和本次资源。清理失败会留下 cleanup_pending，下次运行先重试清理。旧尝试保留在原目录。

`summary` 从全量清单和尝试记录重建结果。`planned` 是预定题数，`scored` 是取得有效评分的题数，`passed` 是评分等于 1 的题数。`reward_coverage`、`conditional_pass_rate`、`manifest_pass_rate` 分别表示 scored/planned、passed/scored 和 passed/planned。尚无评分时，条件通过率为 null。同一题有多次记录时按尝试启动时间排序，较早格式缺该时间时依次使用结束时间和目录时间。每批执行结束时还会写出 `summary.json`。所有预定题均取得有效评分时，命令退出 0，有未完成题或中断时返回非零。

本版 `summary` 和比较程序读取历史记录时，若原生 `outcome.json` 明确给出不同的得分、无效评分或未完成状态，就将该次尝试转为未计分。缺少可选字段的旧记录沿用已接纳的评分。使用本版冻结调度器的批次会在下次运行时重试未计分题；更早冻结的批次仍按当时运行器的恢复规则执行。若尝试记录不能与目录中的批次、任务和尝试编号对应，报告标记 `unverified_identity`，原始文件保留在原处，评分、诊断及环境观测不归入当前批次。

比较只改变输出上限的两个批次时，可生成逐题报告。

```sh
python3 -B validation/terminalbench/compare.py "$BASELINE_RUN" "$CANDIDATE_RUN" > comparison.json
```

比较程序核对两边的任务、资产、配置及记录的调度条件，列出各次尝试的评分、耗时、停止原因、用量和缺失项。`observed_request_count` 按预检或后续事件中出现的逻辑请求 ID 去重。宿主代理单独记录 `provider_http.upstream_attempts`（发起上游网络操作的次数）及 `provider_http.responses`（收到 HTTP 响应的次数）。本地拒绝的请求不计入这两项，旧记录没有此观测时显示 null。若某题任一批次缺少调度参数，`scheduler_checks` 标为 unverified。对老格式批次，比较程序可以核对归档目前的可执行位和目录，但无法还原冻结当时的权限。

每个请求的 `status` 记录已准备、已完成、失败、预检拒绝或取消，`stop_reason` 记录 provider 响应给出的停止原因。旧记录保留原有停止原因，缺少的请求状态显示为 null。每次尝试的 `verify_rc` 单独记录评分脚本退出码，旧版缺失时显示为 null。

每次尝试的 `outcome.json` 记录未计分的阶段。验证器已经返回分数、但 Agent 缺少可计分的终态时，`stage` 指向 `agent`，汇总将分数保留在 `observed_reward`。Harbor 报告异常时保留实际运行阶段；权限异常记录为 `PermissionError`，未知异常统一记录为 `execution_failed`。Agent 或验证器超时的记录另附 `phase_timeout_sec`，取自该题冻结计划的原生阶段时限。

### 选择共享任务的交付物

共享任务没有原生 `artifacts` 时，可在 `init` 命令中重复传入 `--capture`。例如 `--capture polyglot-rust-c=/app/polyglot/main.rs` 选择一个文件，`--capture example=/workspace/generated` 选择一个目录。每次运行只接受已选任务名，路径必须是明确的绝对路径。每题最多 16 个，不能互相覆盖，也不能选择通配符、隐藏文件或运行器和系统控制目录。已有原生交付声明和单独 verifier 的任务沿用题目定义。

列表进入冻结计划的 `capture_paths` 和 `assets/captures/<task>.json` 的 SHA-256 清单。Harbor 在 agent 结束后、共享 verifier 启动前按清单采集，所以 verifier 修改或删除交付文件不会改变收据。每项的 `declaration_index` 对应规范化列表的下标。默认保留文件数量、字节数、哈希及采集状态，设置 `AEGIS_BENCH_ARTIFACT_RECIPIENT_CERT` 可按相同的容量和文件类型限制保存加密副本。

`outcome.json` 的 `artifact_selection` 表明来源。未配置时为 `not_configured`，原生声明为 `task_declared`，操作者选择为 `operator_selected`。已选文件缺失会在相应条目显示 `status=missing`。以前版本只存空数组的记录可以从冻结任务计划核对当时是否选择了输出。用 `python validation/terminalbench/smoke_capture.py --evidence-helper /path/to/aegis-evidence` 可运行不调用模型的 Docker 对照。
