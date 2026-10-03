# Aegis Agent Hooks Spec

## 1. 设计目标

Hooks 是对运行时关键节点的轻量扩展机制，用于：

- 在关键事件上通知外部系统
- 对特定输入或输出做小范围改写
- 在不侵入核心 loop 的前提下加入团队约束

Hooks 不是插件市场，也不是脚本语言运行时。

## 2. 设计来源与裁剪

参考 OpenCode Hooks 的三个核心思想：

- 事件驱动
- 顺序执行
- 多个 hook 共享并改写同一个输出对象

本项目刻意裁剪掉的部分：

- 不做 JS/TS 插件加载器
- 不做 auth hook
- 不做自定义 tool 注入插件
- 不做复杂 hook 分类体系

v1 的目标是“用户可在 YAML 配置中声明 hooks”，而不是复制 OpenCode 的完整插件系统。

## 3. Hook 点

v1 只开放 9 个 hook 点：

- `session.start`
- `session.awaiting_input`
- `session.pause`
- `session.complete`
- `session.fail`
- `user.message`
- `assistant.message`
- `tool.before`
- `tool.after`

## 4. Hook 类型

### 4.1 Event Hook

只读通知型，典型用于：

- 发送通知
- 记录审计
- 调用外部系统

适用点：

- `session.start`
- `session.awaiting_input`
- `session.pause`
- `session.complete`
- `session.fail`
- `assistant.message`

### 4.2 Transform Hook

可修改 payload 的 hook。

适用点：

- `user.message`
- `tool.before`
- `tool.after`

## 5. 配置结构

```yaml
hooks:
  session_complete:
    - name: notify
      command: ["notify-send", "aegis-agent", "session completed"]
  user_message:
    - name: interactive-prefix
      match:
        mode: run
      inject:
        field: text
        prefix: "[interactive]\n"
  user_message:
    - name: block-forbidden-input
      filter:
        field: text
        reject_if_contains: forbidden
```

说明：

- 配置键使用 snake_case
- 内部 hook point 使用点号命名
- 两者一一映射

## 6. Hook 匹配规则

支持的匹配字段：

- `tool`
- `mode`
- `status`

v1 不支持：

- 正则路由
- 表达式语言
- 多层条件布尔组合

未配置 `match` 视为总是匹配。

## 7. Hook 动作

### 7.1 `command`

执行外部命令。

规则：

- 串行执行
- 通过 stdin 向 hook 进程传入当前 payload 的 JSON
- 继承最小环境变量
- 捕获 stdout/stderr
- 结果写入 `events.jsonl`
- 默认不返回给模型
- 默认受 `hooks.default_timeout_sec` 或 hook 自己的 `timeout_sec` 约束

支持变量替换：

- `$SESSION_ID`
- `$WORKDIR`
- `$TOOL_NAME`
- `$STATUS`
- `$FILE`

缺失命令预检：

- 若 hook command 的可执行文件或可静态识别的 shell script 路径缺失：
  - `fail_closed: false` 时写入结构化 `hook.warning` 事件并跳过 command 执行，后续 `inject` / `filter` 仍可继续处理同一 payload
  - `fail_closed: true` 时按 fail-closed 失败处理并阻断当前流程
- 预检只覆盖 argv 形式可直接判断的命令路径和 `sh script.sh` / `bash script.sh` 这类脚本操作数，不尝试完整解析 `sh -c` 字符串
- 预检不能替代 command timeout、输出截断和最小环境变量规则；存在的 command 仍按正常 hook command 路径执行

### 7.2 `inject`

对指定字段做轻量文本修改。

字段：

- `field`
- `prefix`
- `suffix`
- `set`

规则：

- 仅作用于顶层字符串字段
- 若字段不存在或不是字符串，则跳过并记 trace

### 7.3 `filter`

对指定字段执行简单过滤。

字段：

- `field`
- `reject_if_contains`

规则：

- `reject_if_contains` 命中时返回 hook 阻断错误

## 8. 执行顺序

对于同一 hook 点：

- 按配置声明顺序串行执行
- 每个 hook 共享前一个 hook 处理后的 payload

这意味着：

- 第二个 hook 能看到第一个 hook 改写后的输出
- 不需要并发合并语义

## 9. 错误策略

默认：

- fail-open
- 记录 `hook.failed` 事件
- 主流程继续

若 `fail_closed: true`：

- hook 失败会阻断当前流程
- CLI 退出码为 hook 错误

## 10. Payload 规范

### 10.1 `user.message`

- `text`
- `mode`
- `session_id`

### 10.2 `assistant.message`

- `text`
- `session_id`
- `status`

### 10.3 `tool.before`

- `tool_name`
- `arguments`
- `session_id`

### 10.4 `tool.after`

- `tool_name`
- `llm_output`
- `display_output`
- `session_id`

### 10.5 `session.*`

- `session_id`
- `status`
- `workdir`
- `provider`
- `model`

## 11. 安全边界

- hooks 默认在 session 的有效 workdir 执行；自定义相对 command/script 路径也相对此目录解析，不相对 init 目录或配置文件目录
- 不自动提升权限
- 不提供任意内存访问接口
- 不允许直接改 session store 文件
- `command` 必须是 argv 数组，不接受 shell 字符串

### 11.1 init 生成的示例 hook

- `init` 的示例 completion hook 引用实际生成脚本的绝对路径；普通路径直接作为 script argv，不改变自定义 hook 的相对路径语义。
- 生成路径含字面量 `$` 时，以分段 shell 字面量引用该路径，避免既定 hook 变量替换改写安装目录。这类示例使用 `sh -c`，不参与静态 script 预检；脚本删除后记录 command 失败，仍按示例的 fail-open 策略继续。
- 示例脚本把 payload 追加到有效 workdir 下的 `.aegis-agent/hooks/logs/session-complete.jsonl`。
- 仅移动配置文件且原脚本仍存在时，示例引用继续有效；移动或删除原脚本/安装目录后应更新引用或重新生成。既有用户配置不自动改写。
- `--example-hook=false` 不生成示例 command 或脚本。

## 12. 日志与审计

每次 hook 至少写一个事件：

- `hook.triggered`
- `hook.finished`
- `hook.failed`
- `hook.warning`，用于 fail-open 预检跳过等非阻断配置漂移

记录字段：

- hook 名称
- hook 点
- 是否改写 payload
- command 返回码
- fail-open / fail-closed
- 缺失 command/script 的路径与原因（如适用）

## 13. 验收标准

- event hooks 能被触发
- transform hooks 能顺序改写 payload
- `reject_if_contains` 可阻断流程
- command hook 结果被记入事件日志
