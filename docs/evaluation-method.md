# 本地评测方法

本项目的评测事实来源是版本化 JSON Dataset、真实本地 HTTP/SSE Runner 和确定性报告。Runner 只调用现有 `/api/chat`、approval、resume、cancel 路由；它不直接拥有运行状态、Policy、审批、幂等或终态。

## Case 清单

`eval/datasets/synthetic-operations-v1.json` 当前包含八个固定 Case，覆盖四类：

- `positive-audience-query`：只读客群查询。
- `positive-serial-summary`：两步串行查询和汇总。
- `diversity-audience-subject-variant`：变更匿名主体和查询表达的多样性 Case。
- `boundary-approved-outreach`：审批后模拟触达、重复 resume。
- `negative-reject-outreach`：拒绝副作用并保持无写入。
- `negative-unknown-capability`：未知能力被 Policy Gate 拒绝。
- `negative-invalid-input`：空消息在 HTTP 边界被拒绝。
- `boundary-cancel-outreach`：等待审批时取消且不写入。

Case 的 `id`、版本、风险、影响标签、步骤、终态、事件顺序、禁止副作用、timeout、retry budget、evidence requirements 和 evaluator 规则均由人工 JSON 定义。未知字段、重复 key/ID、非法 timeout/retry、未知 fixture/evidence/evaluator 会在加载阶段拒绝。每个可运行 Dataset Case 必须声明四个硬评测维度：`business_correctness`、`architecture_boundary`、`side_effect_safety`、`stability`；Loader 还会按 Case 类型强制注入并校验最小证据矩阵（正常 Run 需要 run state、HTTP status、trace、cleanup、事件序号；预期 Tool/审批路径分别需要 Tool call/approval evidence）。运行时固定执行四个硬维度，Case 声明只作为不可关闭的合同校验；声明但未采集到的证据会产生硬失败，不能因为某个 evaluator 没有看到对应事件而被动通过。

## 报告字段

报告包含 Dataset/Case version、选择的风险与影响标签、`code_version`、生成时间、每个 `run_id`、HTTP 状态、事件顺序、trace ID、终态、耗时、重试计数、fake write count、cleanup result 和四个 evaluator 的独立结果。失败结果保留结构化 `failed_assertion`、failure taxonomy、evidence reference 和可空 human disposition；不写凭证、完整 Prompt、SQL 参数或未脱敏 Tool payload。文件权限为 `0600`。`business_correctness` 硬断言终态、事件顺序及 Case 声明的结构化结果字段，报告仅保留预期字段投影。

四个硬评测维度是 business correctness、architecture boundary、side-effect safety 和 stability。每个 Case 还经过统一 evidence integrity 校验：Evidence 必须关联非空且匹配的 Case/版本/Run，事件必须有非空且唯一的 EventID，Event RunID/trace 必须与 Evidence 关联，sequence 从 1 连续递增；`Terminal` 必须对应唯一 terminal event；Policy/Plan 证据必须发生在 `tool_call` 之前。architecture 使用启动快照中的 Worker allow-list 与 Tool 风险/审批/幂等元数据；side-effect 判断依据合同风险，不依赖某个具体 Tool ID。审批必须精确匹配 `step_id + worker_id + tool_id`，fake write 必须有 side-effect Tool 归因，只读 Tool 不能借用写入证据，同一绑定键重复 side-effect `tool_call` 会硬失败。证据合同还会检查 Tool 调用存在、Policy evidence、事件序号单调、终态唯一、trace、HTTP 状态和 cleanup。退出码 `0` 表示所有 Case 通过，`1` 表示运行或硬断言失败，`2` 表示 Dataset/配置/装配失败。

## 风险与失败回流

PR 风险和影响标签由人声明；无法判断时按 high。高风险变更必须运行全量 Case，并由人工 reviewer 记录批准结论、理由和时间。`eval/feedback.go` 的 `RiskRecord`、`FailureRecord`、`HumanDisposition` 和 `JudgeResult` 只是 JSON 合同：Judge 默认关闭或使用本地 fake，永远不改变硬门禁退出码。

CLI 使用 `-risk low|medium|high` 和 `-impact-tags tag-a,tag-b`；也接受 `EVAL_RISK_LEVEL`、`EVAL_IMPACT_TAGS`。low/medium 运行对应风险基础 Case 加标签匹配 Case，high 或未知风险运行全量。CI 通过 `PR_RISK_LEVEL`/`PR_IMPACT_TAGS` 接线，未设置时按 high 全量运行；托管平台 Required Checks/Reviewers 仍需单独配置。

失败先按业务预期、代码实现、架构边界、评测规则、Case 数据、环境/依赖或 Judge 误判分类。只有人工 disposition 确认后，才允许增加版本化 Case、确定性规则或 Rubric；下一次关联回归必须执行新版本。PR 审批与失败校准是两条独立流程。

## 可选观测

Case 请求会携带低基数的 Case/代码/evaluator 版本到 HTTP span。OTLP/Langfuse、日志文件和 Trace snapshot 只作为诊断证据，不替代本地 JSON 报告；观测后端不可用不得改变业务状态。仓库里的 CI workflow 也不等于托管平台 Required Checks/Required Reviewers 已启用。

## 本地验证

```bash
go run ./cmd/archcheck
go test ./eval/...
go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v1.json -report ./tmp/eval-report.json -code-version dev -config ./config.example.toml
go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v1.json -report ./tmp/eval-report-low.json -risk low -impact-tags audience-query -code-version dev -config ./config.example.toml
```

Langfuse 多角度运行必须在原定运行链路和本地 Case 通过后单独执行、单独记录；配置存在或 exporter 初始化不能作为评测完成证据。

## 反绕过合同

`eval/evaluator/evaluator_test.go` 覆盖规则选择、缺失 evidence、缺失 Policy evidence、非单调序号、重复终态、匿名 side-effect Tool 的审批前置、同一步不同 Worker/Tool 的审批借用、只读 Tool 写入归因和重复 side-effect 调用。它们只验证证据合同，不把 fake write count、Tool 名称或配置字段当成业务执行本身。
