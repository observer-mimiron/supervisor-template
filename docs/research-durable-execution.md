# Durable Execution Research

日期：2026-09-29

本记录只使用当前工作区可访问的本地源码和 Go 模块缓存，没有联网检索。

## 参考与结论

| 参考 | 本地观察点 | 解决的问题 | 本项目结论 |
|---|---|---|---|
| CloudWeGo Eino | Go module cache `v0.9.12`；`compose/checkpoint.go`、`internal/core/interrupt.go`、`adk/runner.go` | 用窄的 CheckPointStore 保存恢复字节，并用 checkpoint ID 让 Runner Resume 继续 | `partial`：Eino 只留在 infrastructure；业务 Run/Step 仍由 Application 的 `ExecutionPlan` 拥有 |
| Coze Studio | `../agent-architecture-references/coze-studio/`，本地 commit `fefb05ff`；`domain/workflow/internal/repo/execute_history_store.go` | 以条件状态更新认领中断执行，避免两个执行者同时推进同一历史 | `implemented`：采用条件 claim 原则，实现为最小 Run lease；不搬 Workflow 平台或队列 |
| ecommerce-customer-service-agent | `../agent-architecture-references/ecommerce-customer-service-agent/`，本地 commit `0c31ebb`；`app/main.py`、`app/order/repository.py`、`app/order/models.py` | 以 thread/checkpointer 恢复图状态，并以数据库唯一键保护副作用幂等 | `partial`：借鉴 checkpoint identity 和幂等键；不引入 Python graph/SQLAlchemy |
| Temporal Go SDK | 本地目录和模块缓存均未发现源码 | 持久工作流、Task Queue、Activity retry/lease 的概念基线 | `deferred`：没有本地源码证据，不声称具备 Temporal 级服务端持久执行 |
| LangGraph | 本地目录未发现上游源码；只看到电商样例的使用侧代码 | thread checkpoint、interrupt/resume、pending writes 的概念基线 | `deferred`：不声称具备其完整跨进程 checkpoint、time-travel 或 pending-writes 语义 |

## 当前实现

- `internal/application/run.Service` 是 Run/`ExecutionPlan` 的唯一状态推进者；Repository 保存请求、审批、Plan/Step 快照，Checkpoint 保存恢复游标，EventStore 保存有序投影。
- `RunLease`/`RunLeaseStore` 已接入 `Start`、`Approve`、`Resume`、`Cancel`。内存适配器用于测试，文件适配器用本地 `flock` 和原子 JSON 替换，MySQL 复用现有 GORM adapter 的条件 upsert。状态写入前后会检查 owner，释放只接受当前 token。
- `ExecutionPlan` 持久化 `Attempts`、`AttemptStatus`、`ErrorClass`、`UpdatedAt`、终态字段。Worker 前先写 `prepared` 和 checkpoint；外部调用开始后写 `running`。接管时 `prepared` 可安全回到 pending，`running` 转为 `waiting_reconciliation`，不会自动重试未知副作用。
- EventStore 负责连续序号、EventID 幂等和单一终态。Resume 以持久 Plan 为准修复缺失的 progress/text/terminal 投影；这是 at-least-once 投影修复，不是跨存储 exactly-once。
- 评测 Runner 只暴露启动快照中的 Tool 风险、审批和幂等元数据；Evaluator 固定执行四个硬维度，Case 的 `evaluator_rules` 只作为不可关闭的声明合同，并把非空 Case/版本/Run 关联、非空唯一 EventID、匹配 Event RunID、`evidence_requirements` 缺失、非单调序号、重复终态、缺失 Policy evidence 和匿名 side-effect 调用作为硬失败。判断依据是合同元数据，不再特判 `simulated_outreach`；非法 risk/tag 选择也不会生成空的假阳性报告。

## 状态标签

- `implemented`：有代码和本地合同测试证明，例如 memory/file lease claim、expiry takeover、owner release、服务级 `RUN_BUSY`、running 接管为 reconciliation、unknown outcome 不重试、事件序号和终态唯一，以及按声明规则执行的本地评测与反绕过证据合同。
- `partial`：已有 adapter 或局部闭环，但缺少同一事务或生产环境证明，例如 Plan/Checkpoint/EventStore 三个存储之间的原子提交，以及生产数据库恢复语义。
- `deferred`：本轮明确不实现，例如 lease renewal、队列/DAG、outbox、跨主机共享文件、外部副作用 exactly-once、Temporal/LangGraph 服务端语义。真实 MySQL 多实例恢复测试不属于当前单实例目标；仅在部署需求转向共享 MySQL lease/多实例时再运行。

## 最终设计边界

本实现选择最小 Run lease，而不是引入队列或通用工作流引擎，因为现有模板只需要串行化一个有界 Run、保护状态 owner 并在崩溃后给出明确人工 reconciliation 入口。租约只解决协作者竞争，不把独立存储伪装成事务。当前以单实例 memory/file 为目标；MySQL GORM adapter 保留为可选订单 Tool 和 lease adapter，但没有完整承载 Run/Plan/Checkpoint/EventStore，也不把多实例数据库恢复列为当前完成条件。
