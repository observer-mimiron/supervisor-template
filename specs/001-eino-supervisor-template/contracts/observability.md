# Observability Contract

本合同只覆盖本地单实例开发、调试和合同测试。观测是旁路能力，不拥有 Run、Approval、
Checkpoint 或终态；任何 exporter、文件或 Collector 失败都不能改变业务结果。

## Context and propagation

HTTP middleware 必须：

1. 从 `traceparent`/`tracestate` 提取合法 W3C remote parent；缺失或非法时创建新的 root span。
2. 生成或复用不含用户内容的 `request_id`，并在响应头 `X-Request-ID` 返回。
3. 创建 `http.server` span，将 `trace_id` 放入 `X-Trace-ID` 和 SSE envelope 的关联字段。
4. 把 OTel context 传入 Application；Application 到 EventStore、Checkpoint、Runner、Tool 和数据库
   的调用不得改用 `context.Background()` 丢失 parent。每次 `/resume` 都可以创建新的 HTTP trace/span，
   但必须通过持久化 `run_id`、可检索的运行关联字段和允许的 span link 关联同一执行；“同一 trace_id”
   只适用于同一 HTTP 请求内的事件，不要求跨 HTTP 请求复用 trace ID。

Langfuse Trace 级属性只能来自可信配置、认证主体或安全摘要，通过 OTel Baggage/attributes
传播：`user.id`、`session.id`、`langfuse.trace.metadata.*`、`langfuse.version`、
`langfuse.release`、`langfuse.trace.tags`、`langfuse.trace.name`。

## Span contract

至少创建以下 span：`http.server`、`agent.run`、`agent.supervisor`/`gen_ai.*`、`agent.worker`、
`agent.tool`、`agent.checkpoint`、`agent.event`，以及 M11 的 `db.query` 和 `db.insert`。

每个 span 至少包含稳定的 `run_id`（若已创建）、`phase`、`outcome` 或 `error_code`；Tool、模型和
数据库 span 还包含对应的 `worker_id`/`tool_id`/`db.operation`。不得记录用户原文、Prompt、
Authorization、API key、SQL 参数值或完整 Tool 输入输出；需要关联时只记录长度、token usage、
digest 或低基数枚举。

## Log contract

日志使用 JSONL 和 `log/slog`。每条运行记录必须能通过 `trace_id`、`request_id` 或 `run_id` 查找，
并包含：

```text
service env level msg trace_id span_id request_id run_id worker_id tool_id
phase error_code error_class retry_decision attempt duration_ms
```

`msg` 是稳定模板；错误全文只允许进入受限本地诊断字段，且必须限长、脱敏并带 fingerprint。
用户消息、Prompt、凭证、内部路径和完整输入输出禁止进入日志。

## Metrics contract

指标至少覆盖 HTTP 请求/延迟/错误、Run 终态、Model/Tool 调用量与延迟、重试、审批等待、恢复、
预算耗尽和观测导出失败。标签只能使用有限枚举（如 route、operation、worker_id、tool_id、
outcome、error_code、backend、signal）；禁止 `run_id`、subject_id、用户输入和错误全文。

## Files and rotation

- 日志文件和 Trace 快照默认 mode `0600`，路径由配置给出，不把凭证写入文件名。
- 按最大字节数或日期轮转，保留数量有限；新文件写入前完成 flush/close，快照以原子 rename 替换。
- Trace 快照只保存 span 元数据、ID、状态、低基数属性和 digest，不保存完整事件、Prompt 或 SQL 参数。
- 文件不可写时，业务继续运行，并产生一次 `observability.degraded` 日志/指标信号。

## OTLP/Langfuse export

Langfuse adapter 使用 OTLP/HTTP `POST {endpoint}/v1/traces`，支持配置化 headers、service name、
resource attributes 和 `x-langfuse-ingestion-version: 4`。认证 header 由环境变量注入，测试使用
HTTP stub 校验 header、trace/resource 属性和 span 到达；不使用真实凭证。

未配置 endpoint、缺少凭证或 exporter 请求失败时：

- 本地文件日志/Trace（若启用）仍尝试写入；
- 产生一次低基数降级信号；
- 不改变 Run/Approval/Idempotency/Checkpoint/SSE 结果；
- 不自动把失败的观测请求当作业务失败重试。

## Error diagnosis

内部诊断至少包含 `error_code`、`error_class`、`phase`、`retry_decision` 和安全 fingerprint；
HTTP/SSE 只返回稳定分类、可理解消息、`run_id` 和 `trace_id`（若已创建）。`unknown_outcome`
必须可在日志、Trace、指标和公开错误中保持同一分类，不能被观测 adapter 改写为普通失败。
