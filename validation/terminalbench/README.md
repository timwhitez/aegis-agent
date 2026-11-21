# Terminal-Bench 本地评测

这个入口在 Linux 上用 Harbor 运行本地的单步 Terminal-Bench 任务。输入是一组受信的任务包、目标架构的 Aegis 二进制和一份 Anthropic Messages 配置。Harbor 负责执行任务定义里的环境、服务、产物交接和评分。每个批次有自己的冻结副本与尝试记录。

## 准备

运行主机需要 Python 3.12 或更新版本、Docker Compose、可用的本地 Docker daemon，以及有足够空间的 `/dev/shm`。安装固定版本的评测依赖，并在 Linux 上构建两个程序。

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r validation/terminalbench/requirements.txt
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -o bin/aegis-agent ./cmd/aegis-agent
CGO_ENABLED=0 go build -trimpath -o bin/aegis-evidence ./validation/cmd/benchevidence
```

配置中的 `default_provider` 指向一个 Anthropic 或 Anthropic-compatible profile。`base_url` 指向 HTTPS 服务，本机测试也支持 loopback HTTP。`api_key_env` 填宿主机上的环境变量名。批次初始化只需要变量名，执行 trial 时才读取其值。

准备好任务目录后，先用 `init --only` 选取小样本。初次运行和恢复都使用同一个 `run` 命令。查看分母、尝试编号和缺失原因使用 `summary`。[批次命令、资源预算与恢复](SCHEDULER.md)

## 诊断与交付物

每个尝试单独保存状态、评分和诊断。宿主上的临时 Unix socket 只向该次尝试提供配置指定的模型与 Messages 接口。真实 provider key 留在宿主。agent 进程通过容器内的回环地址访问该 socket。agent 阶段结束时撤销转发。宿主按上游实际返回的 HTTP 状态和 `Retry-After`（秒数或 HTTP 日期）执行转发，重试由 Aegis 决定。发生重定向时只返回状态码。

证据程序从 Aegis session 中选取请求关联 ID、生效的输出预算、停止原因、已报告的用量和重试结果。宿主再次校验这些字段，再写入尝试目录。原始请求、模型回答、终端输出和密钥不进入诊断文件。每个逻辑请求 ID 可包含多次 HTTP 尝试；通过本地预检后才可能发起上游调用。`outcome.json` 的 `provider_http` 记录该次代理在撤销前发起的上游尝试数和收到响应头的次数。缺失的用量显示为 `null`。取消、启动失败或宿主中断时，尝试记录会标出已知阶段及缺失项。

每个会话的 `phase` 记录采集时的运行阶段。失败会话的 `failure_reason` 只导出已知的运行时原因码，例如 `max_turns_hard_exceeded` 和 `provider_max_tokens`，未识别的失败记为 `other`。截断请求的 `truncated_output` 区分工具调用、可见文本、思考文本、其他内容块及空响应。比较报告用 `null` 表示未采集或不适用的字段。判断续跑是否取得进展，还需结合后续工具调用、产物和评分结果。
新批次使用配套编译的 Agent 与 evidence helper。适配器将 UTF-8 题面上传至容器私有目录，用已打开的文件作为标准输入，并在启动 helper 之前移除文件。Agent 使用 `--input-format raw` 保留首尾空白和换行。阶段时间通过补充系统指令传入。Shell、Docker 客户端、helper 和 Agent 的命令行均不包含题面，题面中提到的服务名不会参与命令行匹配。


Harbor 的原始评分工作目录位于宿主的临时内存文件系统。独立 verifier 只接收声明的产物。原生 shared 任务则需要保留 agent 阶段留下的文件和服务，评分材料在 agent 阶段结束后注入。需要审查评分可信度时，应同时查看环境模式、verifier 证据及尝试状态。

每个批次目录只允许所有者访问（`0700`），新建的诊断和交付副本默认权限为 `0600`。临时原始输出随本次容器清理；中断时，调度器会重试清理。冻结输入、脱敏记录和可选加密副本会保留到操作者删除该批次。复制报告到其他位置时，应单独设置副本权限和保留期限。

`verifier_evidence` 记录 CTRF 用例的状态及标识摘要。有 pytest 文本时，也可用显式输出的 node ID 对账。产物收据按声明顺序对应冻结的 `task.toml`，记录字节数和内容摘要。需要保存可供单独检查的文件时，在宿主上设置 `AEGIS_BENCH_ARTIFACT_RECIPIENT_CERT` 为收件人的 PEM 公钥证书路径。适配器在内存中打包声明的交付物，仅把加密后的 CMS 文件写入尝试目录。每项交付上限为 32 MiB、4096 个条目，超限会留下记录。

采集器对 4 MiB 以内的测试日志逐行检查前 8192 字节，将命令缺失、DNS、下载、TLS 和 pytest 收集错误的命中行数写入 `verifier_evidence.log_signals`。比较报告列出实际扫描的文件数、观察到的用例数和用例状态，得分沿用 verifier 的原始 reward。

收件人可解密一项交付，将 tar 文件的 SHA-256 与收据中的 `private_copy.archive_sha256` 核对后，再检查归档内容。

```sh
openssl cms -decrypt -binary -inform DER -in delivery-000.cms \
  -recip public.pem -inkey private.pem -out delivery.tar
```

任务格式、执行前检查及已支持的 Docker Compose 配置见[任务定义说明](task-format.md)。

## 本地验证

```sh
python -m unittest discover -s validation/terminalbench -p 'test_*.py'
python validation/terminalbench/smoke_docker.py --evidence-helper bin/aegis-evidence
```

容器测试使用合成 agent、共享环境、独立 verifier 和带辅助服务的任务。它会检查参数边界、实际交付的文件、目录排除规则、正式 reward 和持久化日志中的合成敏感标记。
