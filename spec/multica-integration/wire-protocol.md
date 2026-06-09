# Wire Protocol 定义

本文是 `go-cli-agent` 和 Multica `gocli` backend 的唯一耦合点。两端可以独立开发，但必须遵守本协议。

## 1. 版本与边界

- 协议名：`gocli-stream-json`
- 协议版本：`1`
- 传输：子进程 stdin/stdout
- stdout：NDJSON，每行一个完整 JSON object
- stdin：MVP 只写入一条 user message，然后关闭 stdin
- stderr：只用于日志和诊断，禁止输出协议 JSON
- 编码：UTF-8，无 BOM
- 行尾：LF `\n`

MVP 不定义长连接 control/cancel stdin 消息。Multica 的取消、超时和中断沿用现有 backend 模式：取消 Go context，必要时由 `exec.CommandContext` 终止子进程。

## 2. stdout 消息

### 2.1 Envelope

```jsonschema
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "GocliStreamJsonOutput",
  "type": "object",
  "required": ["type"],
  "additionalProperties": true,
  "properties": {
    "type": {
      "type": "string",
      "enum": ["system", "assistant", "user", "result", "log"]
    },
    "protocol": {
      "type": "string",
      "description": "Optional. When present, value is gocli-stream-json."
    },
    "protocol_version": {
      "type": "integer",
      "description": "Optional. Current version is 1."
    },
    "run_role": {
      "type": "string",
      "description": "Optional mission/profile role hint, for example planner, generator, evaluator, orchestrator, worker or validator."
    },
    "metadata": {
      "type": "object",
      "additionalProperties": true,
      "description": "Optional non-transcript metadata such as mission_id, feature_id, milestone_id or parent_session_id."
    },
    "session_id": { "type": "string" },
    "message": { "$ref": "#/$defs/ContentMessage" },
    "result": { "type": "string" },
    "status": {
      "type": "string",
      "enum": ["completed", "failed", "cancelled", "paused", "awaiting_input", "running"]
    },
    "is_error": { "type": "boolean" },
    "usage": { "$ref": "#/$defs/Usage" },
    "handoff": { "$ref": "#/$defs/StructuredHandoff" },
    "log": { "$ref": "#/$defs/LogEntry" }
  },
  "$defs": {
    "ContentMessage": {
      "type": "object",
      "required": ["role", "content"],
      "additionalProperties": true,
      "properties": {
        "role": { "type": "string", "enum": ["system", "assistant", "user"] },
        "content": {
          "type": "array",
          "items": { "$ref": "#/$defs/ContentBlock" }
        }
      }
    },
    "ContentBlock": {
      "type": "object",
      "required": ["type"],
      "additionalProperties": true,
      "properties": {
        "type": {
          "type": "string",
          "enum": ["text", "thinking", "tool_use", "tool_result"]
        },
        "text": {
          "type": "string",
          "description": "text/thinking block payload"
        },
        "id": {
          "type": "string",
          "description": "tool_use id. Must be go-cli-agent provider.ToolCall.ID."
        },
        "name": {
          "type": "string",
          "description": "tool_use tool name"
        },
        "input": {
          "type": "object",
          "description": "tool_use arguments decoded as a JSON object"
        },
        "tool_use_id": {
          "type": "string",
          "description": "tool_result reference to tool_use.id"
        },
        "content": {
          "type": "string",
          "description": "tool_result output text"
        },
        "is_error": {
          "type": "boolean",
          "description": "tool_result error marker"
        }
      }
    },
    "Usage": {
      "type": "object",
      "additionalProperties": true,
      "properties": {
        "input_tokens": { "type": "integer" },
        "output_tokens": { "type": "integer" },
        "cache_creation_input_tokens": { "type": "integer" },
        "cache_read_input_tokens": { "type": "integer" }
      }
    },
    "LogEntry": {
      "type": "object",
      "required": ["level", "message"],
      "additionalProperties": true,
      "properties": {
        "level": { "type": "string", "enum": ["debug", "info", "warn", "error"] },
        "message": { "type": "string" }
      }
    },
    "StructuredHandoff": {
      "type": "object",
      "additionalProperties": true,
      "properties": {
        "summary": { "type": "string" },
        "completed": {
          "type": "array",
          "items": { "type": "string" }
        },
        "remaining": {
          "type": "array",
          "items": { "type": "string" }
        },
        "commands": {
          "type": "array",
          "items": { "$ref": "#/$defs/HandoffCommand" }
        },
        "artifacts": {
          "type": "array",
          "items": { "$ref": "#/$defs/ArtifactRef" }
        },
        "risks": {
          "type": "array",
          "items": { "type": "string" }
        },
        "validation": {
          "type": "array",
          "items": { "$ref": "#/$defs/HandoffValidation" }
        }
      }
    },
    "HandoffCommand": {
      "type": "object",
      "additionalProperties": true,
      "properties": {
        "command": { "type": "string" },
        "exit_code": { "type": "integer" },
        "status": { "type": "string" },
        "artifact": { "$ref": "#/$defs/ArtifactRef" }
      }
    },
    "HandoffValidation": {
      "type": "object",
      "additionalProperties": true,
      "properties": {
        "assertion_id": { "type": "string" },
        "status": { "type": "string", "enum": ["passed", "failed", "skipped", "blocked", "unknown"] },
        "evidence": { "type": "string" },
        "artifact": { "$ref": "#/$defs/ArtifactRef" }
      }
    },
    "ArtifactRef": {
      "type": "object",
      "additionalProperties": true,
      "properties": {
        "kind": { "type": "string" },
        "path": { "type": "string" },
        "uri": { "type": "string" },
        "description": { "type": "string" }
      }
    }
  }
}
```

### 2.2 类型语义

| `type` | 方向 | 必需字段 | 语义 |
| --- | --- | --- | --- |
| `system` | agent -> daemon | `session_id`, `message` | session 已启动。MVP 只要求 `session.started` 输出一条 |
| `assistant` | agent -> daemon | `message` | assistant 文本、thinking 或 tool_use |
| `user` | agent -> daemon | `message` | tool_result。这里的 user 是 LLM transcript role，不是 Multica 用户 |
| `result` | agent -> daemon | `session_id`, `status`, `is_error`, `usage` | 最后一条结果 envelope |
| `log` | agent -> daemon | `log` | 非 transcript 诊断，可忽略 |

### 2.3 输出顺序

推荐顺序：

```jsonl
{"type":"system","protocol":"gocli-stream-json","protocol_version":1,"session_id":"ses_a1b2","message":{"role":"system","content":[{"type":"text","text":"Session started"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","text":"Need inspect the file first."},{"type":"text","text":"I will read the file."}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_01","name":"read_file","input":{"path":"main.go"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_01","content":"package main\n...","is_error":false}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Fixed the issue."}]}}
{"type":"result","session_id":"ses_a1b2","status":"completed","result":"Fixed the issue.","is_error":false,"usage":{"input_tokens":2500,"output_tokens":800,"cache_read_input_tokens":100}}
```

约束：

- `tool_use.id` 必须非空。
- `tool_result.tool_use_id` 必须引用同一 run 内已输出的 `tool_use.id`。
- `result` 必须是最后一条协议消息。
- `usage` 是整个 run 的累计 usage，不是最后一 turn 的 usage。
- consumer 必须忽略未知字段。
- producer 不应输出空 text/thinking block；没有内容时省略 block。

### 2.4 Mission-compatible 可选字段

`gocli-stream-json` v1 支持 mission-compatible profile 所需的 additive 字段，但 MVP producer 可以全部省略，MVP consumer 也必须忽略它们。

| 字段 | 适用 envelope | 语义 |
| --- | --- | --- |
| `run_role` | input/output 任意类型 | 当前 run 的角色 hint，例如 `planner`、`generator`、`evaluator`、`orchestrator`、`worker`、`validator` |
| `metadata` | input/output 任意类型 | 非 transcript 元数据，例如 `mission_id`、`feature_id`、`milestone_id`、`parent_session_id`、`validation_contract_ref` |
| `handoff` | `result` | worker / validator 的结构化交接摘要 |

约束：

- `message.role` 仍是 LLM transcript role；不要把 mission role 写入 `message.role`。
- validation contract 主事实不放在 stdout transcript 中；协议只传 `metadata.validation_contract_ref` 或 handoff 中的 assertion status。
- `handoff.commands[].artifact`、`handoff.artifacts[]` 和 `handoff.validation[].artifact` 只引用本地路径、URL、commit 或 Multica artifact id，不内联大文件内容。
- Multica 可保存 `handoff` 到 mission store；如果当前 Result 类型没有对应字段，应至少忽略它且不影响任务完成。

示例：

```jsonl
{"type":"result","session_id":"ses_a1b2","status":"completed","result":"Feature implemented and validator passed.","is_error":false,"usage":{"input_tokens":4200,"output_tokens":1100},"run_role":"generator","metadata":{"mission_id":"mis_123","feature_id":"feat_login"},"handoff":{"summary":"Implemented login form validation.","completed":["feat_login","VC-001"],"remaining":["Run browser E2E in Multica validator"],"commands":[{"command":"go test ./...","exit_code":0,"status":"passed"}],"artifacts":[{"kind":"report","path":"reports/handoff.json"}],"risks":["Browser flow not checked in this worker run"],"validation":[{"assertion_id":"VC-001","status":"passed","evidence":"go test ./..."}]}}
```

## 3. stdin 输入

### 3.1 Envelope

```jsonschema
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "GocliStreamJsonInput",
  "type": "object",
  "required": ["type", "message"],
  "additionalProperties": true,
  "properties": {
    "type": { "type": "string", "enum": ["user"] },
    "run_role": {
      "type": "string",
      "description": "Optional mission/profile role hint."
    },
    "metadata": {
      "type": "object",
      "additionalProperties": true,
      "description": "Optional non-transcript metadata."
    },
    "message": {
      "type": "object",
      "required": ["role", "content"],
      "additionalProperties": true,
      "properties": {
        "role": { "type": "string", "enum": ["user"] },
        "content": {
          "type": "array",
          "items": {
            "type": "object",
            "required": ["type", "text"],
            "additionalProperties": true,
            "properties": {
              "type": { "type": "string", "enum": ["text"] },
              "text": { "type": "string" }
            }
          }
        }
      }
    }
  }
}
```

### 3.2 输入示例

```jsonl
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Fix the failing test."}]}}
```

Multica 写入该行后关闭 stdin。`go-cli-agent` 将所有 text blocks 用 `\n` 拼接为 runtime prompt。

## 4. `models --json`

`go-cli-agent models --json` 必须输出可被 Multica `[]agent.Model` 等价结构解析的数组。

```jsonschema
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "GocliModelList",
  "type": "array",
  "items": {
    "type": "object",
    "required": ["id", "label"],
    "additionalProperties": true,
    "properties": {
      "id": { "type": "string" },
      "label": { "type": "string" },
      "provider": { "type": "string" },
      "default": { "type": "boolean" },
      "thinking": {
        "type": "object",
        "additionalProperties": true,
        "properties": {
          "supported_levels": {
            "type": "array",
            "items": {
              "type": "object",
              "required": ["value", "label"],
              "additionalProperties": true,
              "properties": {
                "value": { "type": "string" },
                "label": { "type": "string" },
                "description": { "type": "string" }
              }
            }
          },
          "default_level": { "type": "string" }
        }
      }
    }
  }
}
```

MVP 允许只返回当前 config 中可执行 provider 的配置模型；不要求实时调用上游 provider list API。

`id` 是执行路由键，不只是展示名。为避免 go-cli-agent 多 provider config 与 Multica 单一 `opts.Model` 字段冲突，go-cli-agent 发现到的模型必须使用：

```text
<go-cli-agent-provider-name>/<provider-model-id>
```

例如 `anthropic/claude-sonnet-4-6`。Multica `gocli` backend 收到该 ID 后按第一个 `/` 拆成 `--provider anthropic --model claude-sonnet-4-6`；后续 `/` 保留在 model id 中。`/` 对 gocli discovered models 是保留分隔符。如果用户手工输入的 model 不含 `/`，backend 只传 `--model <id>`，让 go-cli-agent 使用默认 provider。

## 5. CLI 契约

| 命令/参数 | 必需 | 语义 |
| --- | --- | --- |
| `go-cli-agent --version` | 是 | 输出包含 semver 的单行版本，例如 `go-cli-agent v0.1.0-dev` |
| `go-cli-agent models --json [--config <path>]` | 是 | 输出模型数组 |
| `exec --output-format stream-json` | 是 | stdout 使用本协议 |
| `exec --input-format stream-json` | 是 | stdin 读取本协议 user prompt |
| `exec --resume <session-id>` | 是 | 恢复旧 session，内部调用 runtime `Continue` |
| `exec --model <id>` | 可选 | 透传 runtime model；为空时省略，让 go-cli-agent 使用自身默认 provider/model |
| `exec --provider <name>` | 可选 | 当 model ID 是 `<provider>/<model>` 时由 Multica 生成，选择 go-cli-agent config provider |
| `exec --workdir <path>` | 是 | 设置工作目录；Multica 同时设置 `cmd.Dir` |
| `exec --timeout <seconds>` | 可选 | go-cli-agent 自身 run timeout；仅在 Multica `Timeout > 0` 时传递，long-horizon profile 可省略 |
| `exec --system <text>` | 可选 | system prompt override |
| `exec --thinking-level <value>` | 可选 | gocli runtime-native thinking level。MVP 为 `low|medium|high|xhigh` |

`--json` 与 `--output-format stream-json` 互斥。

## 6. Exit Code

| exit code | go-cli-agent 状态 | Multica Result.Status |
| --- | --- | --- |
| `0` | `completed` 或 `awaiting_input` | `completed` |
| `130` | `paused` / 用户中断 | `cancelled` |
| `6` | `failed` 且 `last_error` 包含 `incomplete_no_finish` | `failed` |
| `1` | 其他失败 | `failed` |
| 其他非零 | 异常 | `failed` |

Multica 应优先使用 result envelope 的 `status` / `is_error`，并用进程 exit code 作为兜底。

## 7. Usage 映射

| Wire field | Multica `TokenUsage` |
| --- | --- |
| `input_tokens` | `InputTokens` |
| `output_tokens` | `OutputTokens` |
| `cache_creation_input_tokens` | `CacheWriteTokens` |
| `cache_read_input_tokens` | `CacheReadTokens` |

`go-cli-agent` 侧 usage 来源是当前 `turn.stopped.data.usage`，adapter 需要累计每个 turn 的 usage 后写入最终 result。

## 8. 向后兼容

- 新字段只能 additive。
- 消费端忽略未知字段和未知 content block。
- 生产端不得删除或重定义现有字段。
- `session_id` 是不透明字符串。
- `type` 新增枚举值时，旧 Multica backend 应按 `log` 或忽略处理。
