# Data Model

## 核心实体

### ExecutionRequest

一次用户发起的执行请求。

字段：`run_id`、`conversation_id`、`message`、`requested_at`、`resume_of`。

规则：`run_id` 在一次执行生命周期内稳定；用户输入只作为不可信候选输入，不能覆盖策略。

### SupervisorDecision

Supervisor 对请求提出的候选路由。

字段：`worker_id`、`intent`、`arguments`、`risk`、`confidence`、`decision_id`。

规则：必须通过 schema、注册表和 Policy Gate；未知 Worker、未知参数和越权风险直接拒绝。

### ApprovedRoute

Policy Gate 对候选路由的确定性批准结果。

字段：`decision_id`、`worker_id`、`allowed_tools`、`approval_required`、`policy_version`。

规则：只能批准已注册能力；不得由模型直接生成或修改。

### ExecutionPlan / PlanStep

Manager 根据批准路由生成的有界步骤序列。

字段：`plan_id`、`run_id`、`steps`、`max_steps`、`deadline`、`status`；步骤含 `step_id`、`tool_id`、`input`、`status`、`attempts`、`idempotency_key`。

步骤状态：`pending`、`running`、`succeeded`、`failed`、`waiting_approval`、`canceled`。

规则：步骤顺序固定；`succeeded` 不可回写；超出预算、期限或允许重试次数必须终止或等待人工。

### ApprovalRequest

副作用步骤的人工审批记录。

字段：`approval_id`、`run_id`、`step_id`、`action_summary`、`risk`、`status`、`reviewer`、`reviewed_at`。

状态：`pending`、`approved`、`rejected`、`expired`。

规则：只有 `approved` 才能进入 Tool；审批状态由应用代码写入，模型不能生成。

### ToolContract

注册 Tool 的能力和安全声明。

字段：`tool_id`、`input_schema`、`output_schema`、`side_effect`、`risk`、`timeout`、`retry_limit`、`idempotency_required`。

规则：副作用 Tool 必须声明幂等要求；所有 Tool 都必须有白名单、输入校验、超时和错误分类。

### Checkpoint

支持恢复的最小执行快照。

字段：`run_id`、`plan_id`、`next_step_id`、`completed_step_ids`、`status`、`version`、`saved_at`。

规则：只保存恢复所需状态；版本单调递增；不能把 Prompt、凭证或完整敏感上下文写入 checkpoint。

### RunEvent

执行过程的内部事件，之后投影为 SSE。

字段：`event_id`、`run_id`、`sequence`、`type`、`occurred_at`、`data`、`redaction_class`。

公共类型：`started`、`decision`、`plan`、`progress`、`tool_call`、`approval_required`、`text`、`completed`、`failed`、`canceled`。

规则：同一 run 的 `sequence` 单调递增；最多一个终态事件；事件公开数据不得含凭证、Prompt、stack trace 或内部路径。

## 不变量

1. 终态不可变，终态 run 的 resume 只重放原结果。
2. 未注册 Worker/Tool、未批准副作用和不合法状态转换不得执行。
3. 已完成步骤不可重复执行；副作用步骤使用稳定幂等键。
4. checkpoint、审计和事件共享同一 `run_id`，但不成为新的业务状态所有者。
5. 领域类型不引用 Gin、Eino、数据库、MCP 或具体模型 SDK。
