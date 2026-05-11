# Go CLI Agent Terminal TUI Spec

> 当前定位：扩展 phase 规格。terminal TUI 只作为观测面预留，不属于 core v1 默认验收标准。

## 1. 目标

在保留 CLI-first 的前提下，新增一个终端面板式 TUI，用于观察：

- session 列表
- selected session 的状态与最近输出
- child agent / queue job 概览
- 最近事件

它不是 IDE，也不是图形桌面应用；但它是一个真实的终端面板界面，而不是单页文本 dump。

## 2. 命令面

新增命令：

- `go-cli-agent experimental tui`

高频参数：

- `--config`
- `--session`
- `--limit`
- `--once`
- `--refresh-ms`

## 3. 运行模式

### 3.1 interactive

默认模式：

- 需要 TTY
- 使用 alternate screen
- 定时刷新
- 支持键盘导航

### 3.2 snapshot

`--once` 模式：

- 渲染一帧后退出
- 不要求 TTY
- 适合测试、CI、日志采样

## 4. 面板布局

至少包含以下区域：

- `Sessions`
- `Details`
- `Children And Queue`
- `Recent Events`
- `Footer`

## 5. 键盘约定

最低要求：

- `j` / `k` 或上下方向键切换 session
- `g` / `G` 跳到首尾
- `r` 手动刷新
- `q` 退出

## 6. 数据源

TUI 只读取现有文件事实：

- `session.json`
- `state.json`
- `messages.jsonl`
- `events.jsonl`
- `_queue/`

不引入额外数据库，也不依赖进程内独占状态。

## 7. 降级规则

- 非 TTY 且未传 `--once` 时返回明确错误
- 窗口过小时允许裁剪，但不能 panic

## 8. 验收标准

- `tui --once` 能输出稳定面板快照
- interactive 模式可浏览 sessions 并退出
- 新增 TUI 不影响原有文本 / JSON CLI 契约
