# 冻结、运行和恢复评测批次

在安装了评测适配器依赖的 POSIX 主机上，用拥有该批次目录、可以通过本机已有 Docker 入口运行容器的普通用户执行命令。从仓库根目录开始。调度器需要 Python 3.11 或更新版本。准备任务目录、目标平台的 Aegis 二进制、证据采集程序和评测配置。配置中通过 `api_key_env` 指定环境变量名。运行阶段从宿主环境读取其值。

```sh
python3 validation/terminalbench/scheduler.py init \
  --tasks "$TASKS" --run "$RUN" --binary "$AEGIS_BINARY" \
  --evidence-helper "$EVIDENCE_HELPER" --config "$BENCH_CONFIG"
python3 validation/terminalbench/scheduler.py run --run "$RUN" --jobs 4
python3 validation/terminalbench/scheduler.py summary --run "$RUN"
```

`init` 需要一个尚未存在的批次目录。小样本可在此步用 `--only task-a task-b` 固定题目。初始化会复制任务、运行脚本、二进制和脱敏配置，记录运行计划和文件 SHA-256。任务树每批复制一次。只使用来源可信、已审查的任务包与二进制。各题的容器定义和 verifier 脚本会实际执行，冻结只保证批次使用同一版内容。冻结时会拒绝指向题目目录外的符号链接。再次运行该批时，入口先校验摘要，再使用批次内的运行脚本。另开批次即可使用新版脚本或配置。

`run` 默认串行执行。`--jobs` 控制并发上限。`--cpu-budget` 和 `--memory-mb` 可限制并发任务申请的 CPU 核数和内存 MiB，按每题 agent 和 verifier 两阶段中较高的声明值预留。额外 Compose 服务的实际开销需要单独预留。若单题申请超出指定预算，调度器在启动 trial 前报告该题。

每题每次尝试使用 `attempts/<任务名>/<UUID>/`。目录内的 `state.json` 记录批次、尝试标识、退出状态、原因、耗时和评分，`outcome.json` 保存适配器的结果。适配器必须正常返回、状态为 `completed` 并提供有效有限数值，调度器才接纳评分。0 和小数评分都会按原值保留。超时等未采纳尝试若观察到数值，另存为 `observed_reward`。缺失结果、无效基础设施、超时和中断均在下一次 `run` 时重试。成功完成的 0 分和 1 分均可跳过。第二个针对同一批次的执行进程会收到 `run already active`。

收到 SIGINT 或 SIGTERM 后，调度器停止接单，记录取消来源并通知已启动的适配器退出。默认每题硬时限为 agent 声明时限加 verifier 声明时限再加 1800 秒，可用 `--hard-timeout` 覆盖。到达硬时限后停止接单并取消本批其他活跃尝试，尚未开始的题保留为缺失。调度器异常退出时，独立监控进程从关闭的管道识别中断，按批次 UUID、任务名和尝试 UUID 派生的标识清理资源。清理目标从批次清单和目录计算，尝试结果中的字段不能指定另一个批次的容器。清理未完成时，下次运行先重试清理。旧尝试保留在原目录。

`summary` 从全量清单和尝试记录重建结果。`planned` 是预定题数，`scored` 是取得有效评分的题数，`passed` 是评分等于 1 的题数。`reward_coverage`、`conditional_pass_rate`、`manifest_pass_rate` 分别表示 scored/planned、passed/scored 和 passed/planned。尚无评分时，条件通过率为 null。每批执行结束时还会写出 `summary.json`。所有预定题均取得有效评分时，命令退出 0，有未完成题或中断时返回非零。

比较只改变输出上限的两个批次时，可生成逐题报告。

```sh
python3 -B validation/terminalbench/compare.py "$BASELINE_RUN" "$CANDIDATE_RUN" > comparison.json
```

比较程序核对两边的任务、资产、配置及记录的调度条件，列出各次尝试的评分、耗时、停止原因、用量和缺失项。预检事件计的是进入请求准备阶段的次数，实际发出的 HTTP 请求另行记录。
