# CLI 与配置契约

## 命令表

根入口由 `internal/app/app.go` 分发：

| 命令 | 作用 |
| --- | --- |
| `init` | 生成或更新一份配置、provider profile、session/skill 目录和可选示例 hook |
| `models` | 列出当前配置中的 provider/model 记录；不是远端模型发现 |
| `web` | 启动本地 Web 服务；默认 `127.0.0.1:3940`，worker 默认 2，范围 0–8 |
| `run` | 交互执行；TTY 下 Esc 可触发 interrupt |
| `exec` | 非交互执行；可输出 JSON/stream-json，默认要求显式 `finish` |
| `continue` | 读取已有 session，提交普通 follow-up 或显式恢复/审批操作 |
| `steer` | 向运行中的 session 写入 steering，可选择 interrupt 当前执行 |
| `sessions` | 列出 session；`sessions context <id>` 输出上下文报告 |
| `goal` | 查询和改变 Goal、mission plan、validation 和审批 |
| `tasks` | 读取 session 的 todo/task 视图 |
| `probe-provider` | 使用当前或显式 profile 检查 provider 能力和请求 |
| `doctor` | 输出配置、环境和可选 browser 诊断；`--skip-probe` 不调用 provider |
| `experimental` | 进入 `delegate`、`children`、`queue`、`tui` 和 Web 的扩展入口 |

旧的顶层 `delegate`、`children`、`queue`、`tui` 不再是当前命令；必须使用 `aegis-agent experimental ...`。

## 执行命令

常用形式：

~~~sh
./bin/aegis-agent run --config config.yaml "prompt"
./bin/aegis-agent exec --config config.yaml --json "prompt"
./bin/aegis-agent exec --config config.yaml --output-format stream-json --input-format stream-json
./bin/aegis-agent continue <id> --config config.yaml --message "follow-up"
./bin/aegis-agent steer <id> --config config.yaml --message "focus on tests" --interrupt
~~~

`run` 和 `exec` 的重要参数：

- `--config`：选择配置文件；显式缺失会失败；
- `--provider`、`--model`：覆盖当前启动的 profile/model，并记录到 session snapshot；
- `--workdir`：选择本次执行的工作目录；
- `--timeout`：本次运行的超时；runtime command timeout 另由配置控制；
- `--plan`、`--plan-only`：进入 Plan Mode；
- `--goal`、`--goal-mode`、`--goal-token-budget`、`--goal-time-budget`、`--goal-success`、`--goal-validate`：创建 Goal draft；
- `--thinking-level`：请求适合当前 adapter 的思考级别；
- `--json`：输出 JSON；`--output-format stream-json`：输出事件协议；二者互斥；
- `--input-format stream-json`：从 stdin 读取初始 prompt，不再接受位置 prompt；
- `--resume`：仅适用于 `exec` 的已有 session；
- `--allow-builtin-config`：明确允许没有配置文件时使用内置默认；
- `--send-metadata=true|false`：显式覆盖 provider metadata 选项，并可持久化到 resume 事实。

审批/恢复命令还可能需要 `--plan-mode-id`、`--plan-version`、`--expected-revision`、`--approval-request-id`、`--approve-latest` 或 `--approval-receipt`。审批目标必须明确，不能用普通 continue 伪装审批。

## 配置选择

配置 loader 先从内建默认构造一份 Config，再按选择结果读取 YAML 并规范化路径、超时、重试和输出边界。

- 提供 `--config path` 时，只选择这一层；文件不存在、是 symlink 或解析失败都会返回错误。
- 未提供 `--config` 时，loader 检查 home 下的 `~/.aegis-agent/config.yaml`、当前工作区的 `.aegis-agent/config.yaml` 和 `AEGIS_AGENT_CONFIG` 指向的文件；加载报告记录每一层的结果。
- workspace 配置只有父进程设置 `AEGIS_AGENT_TRUST_WORKSPACE_CONFIG=1` 或 `true` 时才会被隐式接受。工作区自己创建的 marker 不能自证可信。
- `AEGIS_AGENT_CONFIG` 是配置选择环境变量，不由 `.env` 注入；`AEGIS_AGENT_ENV_FILE` 可以选择环境文件路径。
- provider 执行、`web`、`probe-provider` 等入口要求至少加载一层配置，或显式传 `--allow-builtin-config`。这条准入规则防止在未审阅的默认 endpoint 上直接发请求。
- 相对的 session、skills 和 isolation 路径按照调用时工作目录解析；配置中的 `~` 扩展为用户 home。

执行前建议用 `doctor --skip-probe` 查看加载报告；它不会为了诊断而调用 provider。

## YAML 顶层结构

~~~yaml
schema_version: 1
default_provider: openai
providers: {}
role_providers: {}
web: {}
session: {}
skills: {}
tools: {}
runtime: {}
output: {}
hooks: {}
~~~

### provider profile

每个 `providers.<name>` 至少需要 `api_provider`、`api_key_env`、`base_url` 和 `model`。可选字段包括 `context_window_tokens`、request/stream timeout、retry、`max_output_tokens`、reasoning/thinking、`store`、`send_metadata` 和 provider-specific header/format。

当前 adapter family：

- `openai-compatible`：Responses wire API；OpenAI 以及兼容 Responses 的网关；
- `anthropic-compatible`：Anthropic Messages；
- `google`：Gemini `generateContent`。

`wire_api` 是兼容字段，OpenAI-compatible 当前接受 `responses`。不能在文档或样例中承诺旧的 chat-completions 入口。

### Web 与本地文件

- `web.basic_auth.username/password_hash` 要么同时配置，要么都不配置；password 必须是 bcrypt hash，服务不会启动 HTTPS。
- `web.allowed_hosts` 扩展 Host allowlist；localhost、`*.localhost` 和 IP literal 默认允许。
- `session.dir` 默认 `.aegis-agent/sessions`，`dir_mode` 默认 `0700`。
- `skills.dirs` 默认 `./skills`，支持多个目录；skill 正文和 command tools 仍受 catalog trust 规则约束。

### runtime

常见配置：

- `exec_finish_required`：`exec` 是否要求显式 finish；
- `command_timeout_sec`、`shell_env_allowlist`、`exec_policy.mode`、`shell.sandbox`：shell 执行边界；
- `provider_auto_resume`、`steer`：provider retry/recovery 和 steering；
- `compact`、`tool_output`、`ephemeral`：上下文、tool output 和 artifact 限额；
- `multi_agent`、`isolation`、`queue`、`child_budget`：child/queue 扩展的并发、目录、lease 和预算；
- `hooks`、`degeneration`、`pre_completion`、`ralph_loop`：runtime 生命周期和质量辅助行为。

默认值以 `internal/config/config.go` 为准。`config/config.example.yaml` 是可编辑样例，不能替代 Config 默认构造器。

## 凭据与环境文件

provider profile 只保存环境变量名，例如：

~~~yaml
api_key_env: OPENAI_API_KEY
~~~

真实值可来自父进程环境或项目 `.env`。`.env` 由 Aegis 按键值文件读取，不执行 shell；已有非空进程环境优先。受控写入会拒绝 symlink、非 regular file 和不合规 key。不要把 API key 直接写进 YAML、README、测试 fixture 或提交。

## 输出与诊断

- 默认输出为 text；`--json` 用于单次结果；`--output-format stream-json` 用于事件流。
- 诊断信息写 stderr，模型结果和协议记录写 stdout，便于脚本分离两者。
- `run`/`exec` 会显示已加载的配置层、解析出的 profile/model/endpoint origin；URL 的 path、query、fragment 和 userinfo 会隐藏。
- `doctor --skip-probe` 适合在没有 key 或网络时检查配置；`probe-provider` 会实际尝试 provider，必须明确说明它可能产生网络请求。

## 兼容项

`web.legacy_ui_enabled` 仍可读取旧 YAML 以避免严格解析失败，但被忽略、不会写回、不会出现在 Web Settings/API 中。旧 Web 页面和旧资源路由已经删除，回滚应使用 Git 版本操作，不应添加运行时 toggle。
