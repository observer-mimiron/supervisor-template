# Runtime Foundation Contracts

这些接口是应用层合同。具体的 Eino、Gin、MCP、文件、Redis、数据库或向量实现只能位于
`internal/infrastructure`，不得把其类型泄漏到 domain。

## ErrorPolicy

```text
Classify(error, phase) -> ErrorClass
Decide(ErrorClass, attempt, retry_limit, context) -> RetryDecision
```

`phase` 至少区分 `pre_call`、`in_flight`、`commit` 和 `projection`。
`ErrorClass` 至少包括 `pre_call_failure`、`timeout`、`canceled`、`invalid_output`、
`policy_denied`、`business_failure`、`unavailable`、`unknown_outcome` 和 `internal`。

约束：

- 只有 `pre_call_failure` 且未开始外部调用时允许重试。
- `in_flight` 或提交确认缺失必须得到 `unknown_outcome`，进入 `waiting_reconciliation`。
- `context.Canceled` 只有在 Runner/Tool 观察到取消后才能收口为 `canceled`。
- HTTP、SSE、日志和重试决策使用同一分类，公开映射不能泄露内部错误详情。
- 诊断字段至少包含 `error_code`、`error_class`、`phase`、`retry_decision` 和安全 fingerprint；
  观测 adapter 不得重新分类业务错误。

## ObservationContext

所有应用 Port 接受调用方传入的 `context.Context`。HTTP middleware 建立 OTel server span 后，
Application 必须把同一 context 传给 Runner、Tool、Checkpoint 和 EventStore；任何实现不得以
`context.Background()` 替换已有 parent。EventStore 合同应等价于
`Append(ctx context.Context, event agent.RunEvent) error`。

观测失败是旁路故障：日志、Trace、Metric 或 Langfuse exporter 返回错误时，Application 继续按
原有状态、审批、幂等和终态合同运行，并只发出一次低基数 `observability.degraded` 信号。

## ToolInvoker / ToolPool

```text
Prepare(ctx, approvedRoute, planStep) -> ToolInvocation
Acquire(ctx, toolID, runID, deadline) -> ToolPoolLease
Invoke(ctx, lease, invocation, input) -> ToolResult | error
Release(ctx, lease) -> error
```

Pool 必须在外部调用前检查注册 ID、输入 Schema、allow-list、审批状态、deadline、并发配额
和幂等键。Pool 负责实例生命周期、健康和资源计量，不负责 Policy Gate、审批和终态。

未注册、越权、非法输入或配额耗尽必须在 `Invoke` 前拒绝；超时、协议错误、不可用和业务
错误必须可被 `ErrorPolicy` 区分。`Release` 幂等，租约失效不得继续调用。

## MemoryStore / MemoryRetriever

```text
Append(ctx, scope, entry) -> MemoryEntry
Expire(ctx, now) -> count
Query(ctx, MemoryQuery) -> []MemoryEntry
```

实现必须按 `tenant_id + subject_id + conversation_id + kind` 隔离，执行 TTL、大小、敏感字段
过滤和召回上限。Memory 不能写入 `ExecutionPlan`、修改权限或代替 checkpoint；应用层决定何时
把召回内容交给模型。默认实现为内存/文件，持久化、向量检索和外部记忆服务是可替换适配器。

## WorkerRunner

```text
Run(ctx, RunnerSession, ApprovedRoute, PlanStep) -> StepResult
Resume(ctx, RunnerSession, Checkpoint, PlanStep) -> StepResult
```

Application/Manager 负责推进完整 `ExecutionPlan`，Runner 只执行当前已批准的一个 `PlanStep`，
不能新增步骤、Tool、审批或终态事件。应用服务负责状态转换、checkpoint、幂等、错误策略和
唯一终态；Runner 只返回步骤结果、候选文本和可分类错误。
函数式 fake Runner、普通函数 Runner 与 Eino ReAct/Graph adapter 必须共用这组合同测试。

## 取消、预算和未知结果

- 每个 run 有步骤数、Tool 调用数、模型循环数、deadline 和可选成本预算；任何预算耗尽都由
  应用服务写入唯一终态或等待状态。
- 取消通过 per-run `context.Context` 传播；客户端断开不等于服务端取消。
- 外部调用已开始但结果未确认时返回 `RUN_OUTCOME_UNKNOWN`；恢复只能人工核对或明确终止，
  不得自动再次调用副作用 Tool。
- Runner、Tool 和模型适配器不得直接追加 `RunEvent`；事件由应用层根据状态变化投影。
