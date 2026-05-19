# Real Task Validation

本目录承载 `go-cli-agent` 的真实任务验证资产：

- `config.openai-compatible.yaml`: 验证专用配置
- `scenarios.md`: 场景设计与执行口径
- `runs/`: 每次真实执行的记录
- `skills/`: 验证时需要安装的本地 skills
- `workspaces/`: 用于稳定复现的任务工作区夹具

默认执行方式：

```sh
cd go-cli-agent
export OPENAI_API_KEY=...
./bin/go-cli-agent doctor --config validation/config.openai-compatible.yaml --provider openai-compatible --json
```

完整复杂矩阵默认入口：

```sh
cd go-cli-agent
export OPENAI_API_KEY=...
./validation/run_round31_complex_real_matrix.sh
```

偏真实开发任务族的 task-heavy live 矩阵入口：

```sh
cd go-cli-agent
export OPENAI_API_KEY=...
./validation/run_round61_task_heavy_real_matrix.sh
```

统一 acceptance stack 入口：

```sh
cd go-cli-agent
export OPENAI_API_KEY=...
./validation/run_openai_compatible_acceptance_stack.sh
```

Web-first console / retry-resume / queue follow-up 的稳定 live 入口：

```sh
cd go-cli-agent
export OPENAI_API_KEY=...
./validation/run_webconsole_followup_validation.sh
```

说明：

- `validation/config.openai-compatible.yaml` 会启用 `Responses API`、有限 provider retry，以及适合长场景矩阵的超时窗口
- `validation/scenarios.md` 记录当前 26 场景复杂矩阵的设计口径
- `validation/task_heavy_scenarios.md` 记录新的 task-heavy 20 场景矩阵口径；它更偏真实复杂开发任务族，并把每个场景的 prompt/raw/artifact/post-check/session evidence 落到独立 case 目录
- `run_round31_complex_real_matrix.sh` 是当前最完整的 26 场景矩阵入口；它会继续执行后续场景，即使单个场景失败；每个场景的状态、tail output、问题线索都会落在独立 run 目录里
- `run_round61_task_heavy_real_matrix.sh` 是当前面向“真实复杂开发任务族”的 task-heavy live 入口；它复用已有 workspaces，但把 `patch` / `patch_go` / multi-package repair / interrupt-resume-completion / role-aware foreground delegate / role-aware background queue / focused webconsole operator proof 收口成约 20 个真实任务场景，并把每个场景单独写到 `cases/<case-id>/`
- 该 task-heavy 入口现在会额外生成 `notes/case-buckets.md`，把 repaired seeded defect、review boundary、workflow proof、owning-runtime proof 四类 case 做 run 级分桶，方便 TT20 和人工 readiness 复盘快速定向
- 对 TT12 / TT14 / TT18 / TT19 这类 delegated、background 或 focused-subrun case，父级 artifact 现在会补入决定性 child/subrun snippet，而不是只指向下游 artifact 路径
- 该主矩阵现在会额外执行一组 task-heavy + RT21 gap-proof preflight tests，并生成 `notes/preflight-task-heavy-proof-tests.md` 与 `notes/preflight-gap-proof-summary.md`，把 provider metadata/retry durability、review artifact enforcement、report prevalidation/path hardening、exact-template guard，以及 interrupt/queue/delegate/project-memory refresh 这些关键 proof 先落成脚本层直接证据，而不是只依赖后续 audit prose
- `run_openai_compatible_acceptance_stack.sh` 是当前长期稳定的一键 acceptance 入口；它会先跑 `run_round31_complex_real_matrix.sh`，再跑 `run_webconsole_followup_validation.sh`，并把两段结果索引到单独 bundle run 目录下；旧 `run_experimental_webconsole_followup_validation.sh` 仅作为兼容 wrapper 保留
- 该 acceptance 入口现在会先做 bundle 级 `probe-provider` preflight；若 probe 在允许的重试次数内仍失败，bundle 会写 `ABORTED.md`、记录 `notes/preflight-index.tsv`，并直接跳过 matrix 与 focused follow-up，避免在明显的网关/认证故障上继续消耗 live 预算
- `live_smoke.sh`、`run_openai_compatible_acceptance_stack.sh`、`run_round31_complex_real_matrix.sh` 和 `run_webconsole_followup_validation.sh` 现在都支持 `GO_CLI_AGENT_LIVE_RESPONSES_URL`；切换 live gateway 时优先用这个环境变量覆盖，而不是手改脚本
- `run_webconsole_followup_validation.sh` 是当前稳定的 focused live 入口：它用一个本地 fault-injection proxy 稳定触发 `continue` 阶段的 `provider.retry`，会先制造一个真实 failed queue canary 作为 durable evidence，然后在真实 Web-first console 运行并完成 API-backed queue job 后强制一次 stale-running reconcile，检查 parent `background.jsonl` 仍按 `queue_job_id` 去重
- 主矩阵现在额外覆盖两条 steer proof：一条验证 oversized steer 会在 queue 前直接返回结构化 JSON 错误，另一条验证 interrupt steer 会在延迟 provider call 上写出 `provider.cancelled` durable event，并留下 transport-side cancellation evidence
- 同一个脚本还会验证 embedded shell / JS / CSS 资产可被本地 service 提供，并用 headless Chrome 跑一条真实浏览器交互链，覆盖 Settings / Workspace / Skills / Sessions / Session 导航、主 prompt start、durable session chrome、tool cards、timeline 可见性、history clear、API-backed queue job 完成，以及 queue link 可用时当前 session Background inspector 的 selected job facts 面板；standalone Overview / Worker Pool drilldown、queue notification dedup、retry proof 和 worker API 行为由同一脚本的 API / 文件事实检查或服务层测试覆盖
- focused run 目录通常由 `.gitignore` 忽略，不把某个历史 run 目录当作当前 checkout 的 tracked 证据；需要参考时运行 `validation/run_webconsole_followup_validation.sh <run-id>` 重新生成
- 对 retry-resume proof，这条脚本当前采用“evidence-first”判定：只要 durable session metadata 仍保留原始 `retry_policy.max_attempts=2`，且 resumed turn 真实写出 `provider.retry`，就视为 retry-drift 修复已被证实；即使 bounded finish nudges 之后 session 仍停在 `awaiting_input`，也只作为 run 备注记录，不再阻断后续 UI / queue 验证
- 该脚本需要 `node` 与本地 Chrome/Chromium；默认会自动探测 `google-chrome`、`chromium`、`chromium-browser`，也可通过 `CHROME_BIN=/path/to/browser` 显式覆盖
- 旧入口 `run_round53_retry_resume_and_webconsole_queue_followup.sh` 仍保留为兼容别名，方便复用既有 run-id 命名和历史笔记
- 该脚本还会对每个 live 场景启用首轮无进展 watchdog，避免 provider 在 `provider.call` 后长时间悬挂时拖死整轮矩阵
- 每次执行都会把原始 JSONL、artifact、notes、summary 和 `ISSUES.md` 记录到独立 `validation/runs/<run-id>/`；成功路径的 `ISSUES.md` 明确写入 no open issues
- 面向 OpenAI-compatible 部署与 operator 自检的说明在 [`../docs/openai-compatible-operator-guide.md`](../docs/openai-compatible-operator-guide.md)

该目录只服务于验证，不改变 Web-first v1 主产品叙事。
