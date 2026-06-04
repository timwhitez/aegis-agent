# Multica gocli/Codex 适配与任务停滞修复方案

目标：彻底解决当前 Multica issue 暴露的 gocli 鉴权/模型漂移、错误 runtime 注册、Worker 排队不均、任务执行后未 finish、mention 丢失、repo blocker 不清晰、mission/publish gate 缺失和 skill 绑定不完整问题。本文记录问题上下文、修复点、启动要求和真实验收证据。

## 1. 当前结论

- gocli 必须复用远端 Codex 配置：`base_url=http://127.0.0.1:8327/v1/`、`wire_api=responses`、`model=gpt-5.5`、`reasoning_effort=xhigh`，API key 从 `~/.codex/auth.json` 导出为 `OPENAI_API_KEY`，禁止打印 secret。
- gocli 任务只扫描 Multica workspace 注入的 `./skills`。远端 `~/.codex/skills` 已归档为 `/data00/home/guangzhe.zhang/.codex/skills.disabled-20260604T062039Z`，不能重新纳入 gocli task skill path。
- 正确 online runtime 池是：`codex-general=11`、`gocli=4`。旧 `codex` 和混合注册产生的错误 `gocli` runtime 不应保持 online；若无 `agent`、`agent_task_queue`、`chat_session` 引用，可删除。
- `keep_recent_tool_results: 3` 只影响 go-cli-agent compaction 后 provider view 中保留多少条完整 tool result，不影响 timeout、turn limit、finish、scheduler 或任务恢复。生产默认保持 `3`。
- completion nudge 不应写入 go-cli-agent core 的 Multica 专用流程；Multica 兼容应在 issue-bound Completion Contract、task runtime brief、skill/handoff contract、side-effect recovery 中实现，Codex General 和 gocli 共用。

## 2. 问题与修复

| 问题 | 上下文 | 根因 | 修复 |
| --- | --- | --- | --- |
| gocli `Invalid API key` / 模型漂移 | `CyberSec Long Horizon Master` 曾报 `openai: {"error":"Invalid API key"}`。 | gocli daemon 没有稳定复用 Codex base/key/model/reasoning；启动环境可能丢 key。 | 远端 `/data00/home/guangzhe.zhang/.go-cli-agent/config.yaml` 固定 OpenAI-compatible Codex base、`gpt-5.5`、`xhigh`；gocli daemon 从 `~/.codex/auth.json` 导出 `OPENAI_API_KEY`；health preflight 校验 key env、base、model、xhigh、wire API。 |
| 错误 runtime 行反复出现 | 曾出现 `Codex (n37-226-142)` 和错误 `Go CLI Agent` 行。 | `.env` 在 overrides 之后被 source 会恢复 `MULTICA_CODEX_PATH=/usr/bin/codex`；同一 daemon 先注册 `codex+gocli` 后再只注册 `gocli` 时，旧 provider 没被下线。 | `.env` 改为默认禁用旧 `codex` 和默认 gocli；daemon register 增加 provider reconciliation，把同 daemon 未上报 provider 标 offline；无引用的错误 `codex`/旧 `gocli` runtime 已删除。 |
| codex-worker 只注册成 1 个 runtime | 10 个 `codex-worker-*` 进程曾全部显示同一个默认 daemon_id。 | 启动 codex worker 时没设置独立 `MULTICA_DAEMON_ID`。 | 每个 `codex-worker-N` 必须设置 `MULTICA_DAEMON_ID=codex-worker-N`、独立 `MULTICA_AGENT_RUNTIME_NAME`、`MULTICA_WORKSPACES_ROOT` 和 health port。 |
| Worker 1 busy 时仍排 Worker 1 | issue `78ea40b0-89e3-4c9c-96fc-672dd6222b2f`、`c66fefbe-0576-44c5-966b-a170ced1cb20` 暴露 Master exact mention Worker 1，Worker 2 idle 也不接活。 | claim 阶段不能改变已绑定 agent；问题在 task 创建前缺 role/load delegation。 | 增加 squad delegation API/CLI 和 member status，Master 对新独立 slice 使用 `role=worker + least_busy`，exact mention 仍严格绑定指定 Worker。 |
| gocli 写完产物后未 finish | 部分任务已写 artifact/comment，但最后被 `max_turns_hard_exceeded` 或无 final result 标失败。 | 仅靠 hard turn limit 收口不可靠；Multica 未识别“已有副作用但未 finish”。 | gocli 全局 `runtime.max_turns_hard: -1`；Multica 增加 no-final-result hardening、side-effect-aware recovery 和 issue-bound Completion Contract。 |
| repo blocker/no_action 反复 | `repo checkout` 曾报 `MULTICA_DAEMON_PORT not set` 或 `repo is not configured for this workspace`，Master 后续 no_action。 | gocli shell env allowlist 漏 `MULTICA_*`；repo resource 缺预检；blocker metadata 不统一。 | gocli allowlist 加入必要 `MULTICA_*`；新增 `multica repo preflight`；blocker kind/metadata/activity 标准化。 |
| Validator mention 丢失 | LOC-21 / `ad5f6214-a114-409b-881b-9d6e6221579f` 中 Validator 后续复核没有转成 task。 | 同 issue + 同 agent 已有 pending task 时，后续 mention 被静默跳过。 | 增加 mention/follow-up work item 队列，记录 `enqueued/attached/deferred/deduped/skipped/failed`，前序 task 完成后可转 task 或给出明确 outcome。 |
| mission/publish gate 不完整 | LOC-21 缺 Worker 2 Validator review、final report、Lark URL 后仍可能汇总停滞。 | issue-level durable mission state 不足。 | 增加 mission reconcile/recover/complete/publish 能力；close/final 前检查 Validator artifact、final report、publish receipt、`published_doc_url`。 |
| skill roles/bindings 不完整 | `cybersec-long-horizon-mission`、`timwhite-v2-codex-security-review`、`lark-markdown-upload` 角色分配不完整。 | Worker/Validator/Master 不能稳定看到 handoff、receipt、publish contract。 | `cybersec-long-horizon-mission` 的 `multica.roles` 加 worker；`timwhite-v2-codex-security-review` 分配给代码审计 Worker 和 CyberSec Long Horizon Worker；`lark-markdown-upload` 挂给 Master/Validator。 |

## 3. 生产配置要求

远端 gocli config：`/data00/home/guangzhe.zhang/.go-cli-agent/config.yaml`

```yaml
providers:
  openai:
    api_provider: openai-compatible
    api_key_env: OPENAI_API_KEY
    base_url: http://127.0.0.1:8327/v1/
    model: gpt-5.5
    wire_api: responses
    reasoning_effort: xhigh
runtime:
  max_turns_hard: -1
  shell_env_allowlist:
    - PATH
    - HOME
    - LANG
    - TERM
    - GO_CLI_AGENT_CONFIG
    - MULTICA_TOKEN
    - MULTICA_SERVER_URL
    - MULTICA_DAEMON_PORT
    - MULTICA_WORKSPACE_ID
    - MULTICA_AGENT_NAME
    - MULTICA_AGENT_ID
    - MULTICA_TASK_ID
    - MULTICA_TASK_SLOT
  compact:
    input_char_threshold: 1033600
    keep_recent_tool_results: 3
    hysteresis_delta_chars: 258400
    context_profiles:
      openai/gpt-5.5:
        input_char_threshold: 1033600
        keep_recent_tool_results: 3
        hysteresis_delta_chars: 258400
skills:
  dirs:
    - ./skills
```

Codex 对齐来源：`/data00/home/guangzhe.zhang/.codex/config.toml` 已验证为 `model_provider=custom`、`model=gpt-5.5`、`model_reasoning_effort=xhigh`、`base_url=http://127.0.0.1:8327/v1/`、`wire_api=responses`。

## 4. 启动规则

- 先 source `.env`，再 export runtime-specific overrides；不要在 overrides 后再次 source `.env`。
- 默认 daemon 和 `codex-worker-1..10` 只暴露 `codex-general`：
  - `MULTICA_CODEX_PATH=/nonexistent/multica-disabled-codex`
  - `MULTICA_GOCLI_PATH=/nonexistent/multica-disabled-gocli`
  - `MULTICA_CODEX_GENERAL_PATH=/home/guangzhe.zhang/.local/bin/codex-general`
- `codex-worker-N` 必须有独立 `MULTICA_DAEMON_ID=codex-worker-N`、`MULTICA_AGENT_RUNTIME_NAME=codex-worker-N`、`MULTICA_WORKSPACES_ROOT=/data00/home/guangzhe.zhang/multica_workspaces_codex_general_N`。端口：1-9 为 `19851..19859`，10 为 `19899`。
- `gocli-worker-1..4` 只暴露 gocli：
  - `MULTICA_CODEX_PATH=/nonexistent/multica-disabled-codex`
  - `MULTICA_CODEX_GENERAL_PATH=/nonexistent/multica-disabled-codex-general`
  - `MULTICA_GOCLI_PATH=go-cli-agent`
  - `GO_CLI_AGENT_CONFIG=/data00/home/guangzhe.zhang/.go-cli-agent/config.yaml`
  - `OPENAI_API_KEY` 从 `~/.codex/auth.json` 导出，禁止打印
  - daemon id 使用 `/data00/home/guangzhe.zhang/.go-cli-agent/multica-daemon-ids/gocli-worker-N.id`
  - 端口：`19846..19849`
- `AGENTS.md` 已补充上述启动经验；远端 `CLAUDE.md` 已按用户要求直接由 `AGENTS.md` 覆盖，两者内容一致。

## 5. 真实验收证据

运行态：

- 完整重启后 DB runtime 计数：`online|codex-general|local|11`、`online|gocli|local|4`，没有 online `codex`。
- 4 个 gocli health 端口 `19846..19849` 均为 `runtime_preflight.status=ok`，并显示：
  - `api_key_env=OPENAI_API_KEY`
  - `api_key_env_present=true`
  - `base_url=http://127.0.0.1:8327/v1/`
  - `model=gpt-5.5`
  - `reasoning_effort=xhigh`
  - `wire_api=responses`
  - `max_turns_hard=-1`
  - `keep_recent_tool_results=3`
- daemon env 抽查：gocli worker 禁用 codex/codex-general，只启用 `MULTICA_GOCLI_PATH=go-cli-agent`；codex-general worker 禁用 old codex/gocli。
- 日志检索：`/tmp/multica-daemon-*.log` 和 gocli logs 中未发现 `Invalid API key`。
- backend health：`http://127.0.0.1:8080/health` 返回 `{"status":"ok"}`；Web `http://127.0.0.1:3000/login` 返回 401，符合 Basic Auth 预期。

真实 gocli 执行：

- 直接 gocli E2E：真实 `OPENAI_API_KEY` + Codex base 下执行 `go-cli-agent exec -thinking-level xhigh`，返回 `gocli-real-e2e-ok`，stream-json final `status=completed`。
- Multica E2E：创建真实 issue `LOC-23` / `40149e4f-9de3-46c2-8484-20ca9517cdaf` 分配给 `CyberSec Long Horizon Worker 2`。
  - task：`3f46c61f-9815-4b63-8bbd-31578576c230`
  - runtime：`gocli-worker-3` / `53994992-1b61-49ef-a3bc-ddd5e964ac87`
  - daemon 命令含 `go-cli-agent ... --provider openai --model gpt-5.5 --thinking-level xhigh`
  - task completed，issue comment 写入 `gocli-multica-e2e-ok`

测试和构建：

- Go 回归通过：
  - `go test ./internal/daemon ./internal/daemon/execenv ./internal/handler ./internal/service ./internal/mission ./pkg/agent ./pkg/taskfailure ./cmd/multica ./cmd/server -count=1`
- Go 构建通过：`make build`
- 前端类型检查通过：`pnpm typecheck --force`
- mobile 类型检查通过：`pnpm --filter @multica/mobile typecheck`
- 强制生产构建通过：`pnpm build --force`
  - 已知非阻断警告：landing download 页 GitHub API 403；desktop dynamic import warning。

## 6. 回滚点

- gocli config 出问题：恢复 `/data00/home/guangzhe.zhang/.go-cli-agent/config.yaml` 备份，gocli preflight degraded 时禁止 claim 新任务。
- runtime 注册出问题：先检查 `agent`、`agent_task_queue`、`chat_session` 引用；无引用错误 runtime 可删除，有引用则保持 offline/hidden，避免破坏历史。
- delegation 出问题：Master 临时回退 exact mention；保留 member status 只读观测。
- mention/follow-up 队列出问题：不删除历史 comment/task，只追加 outcome 修正记录。
- mission/publish gate 出问题：允许人工补 `published_doc_url`/receipt metadata，但必须保留 blocker/gate activity。
