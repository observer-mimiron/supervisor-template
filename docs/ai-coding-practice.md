# AI Coding 实践证据

本项目展示的是“AI 生成代码，但由合同和验证控制边界”的开发方式。它不把 Prompt 技巧或模型输出当作工程能力证据。

## 工作流

```text
读取宪法、架构和相关 Spec
  -> 明确状态 owner、权限边界和不做范围
  -> AI 做小范围实现
  -> 补合同测试、失败路径和可重放入口
  -> 运行 go test / race / archcheck / smoke / eval
  -> 人工检查 diff、依赖方向、状态推进和副作用
  -> 失败后补规则或 Case
  -> 更新 PROGRESS.md，并保留 partial/deferred
```

## 仓库里的对应证据

| 实践 | 证据 |
| --- | --- |
| 先读边界再写代码 | `AGENTS.md`、`.specify/memory/constitution.md`、`docs/architecture.md`、`specs/001-eino-supervisor-template/` |
| AI 改动受稳定合同约束 | `internal/domain/agent`、`internal/application`、`schemas/`、`prompts/` |
| 跨层行为有可重放验证 | `internal/interfaces/http/*_test.go`、`internal/application/run/*_test.go`、`eval/runner` |
| 架构规则进入自动检查 | `cmd/archcheck`、`.github/workflows/ci.yml` |
| 失败可以定位和回流 | `eval/evaluator`、`eval/feedback.go`、`docs/evaluation-method.md` |
| 未完成能力保持诚实状态 | `PROGRESS.md`、`tasks.md`、`docs/current-capability-and-gap-report.md` |

## 真实失败案例

外部 Tool 调用已经开始、结果提交却未确认时，系统不会把它当作普通可重试错误。`OutcomeUnknownError` 会把运行放入 `waiting_reconciliation`，重复 Resume 不再次调用 Runner；合同测试 `TestUnknownRunnerOutcomeWaitsForReconciliationAndNeverRetries` 会检查这一点。

这个案例说明了为什么“功能测试全绿”不足以证明架构安全：成功路径可以正常返回，但副作用窗口需要故障注入、错误分类和恢复语义共同覆盖。修复落在共享的应用层错误策略，而不是只在某个 HTTP 调用方加一次判断。

## 验收分工

- **AI：** 生成实现、扫描调用链、补局部测试和文档草稿。
- **人：** 定义宪法和 Spec、确认风险等级、检查 diff、确认状态 owner、副作用边界和验收证据。
- **硬门禁：** `go test ./...`、`go test -race ./...`、`go run ./cmd/archcheck`、`go vet ./...`、本地 Case Runner。
- **软证据：** OTLP/Langfuse、Judge 和人工评审只补充诊断，不替代本地确定性合同。

当前工作树实际跑通了全量 Go 测试、race、build、vet、archcheck 和 8 Case 高风险评测；这些结果说明当前范围可复现，不说明项目已经具备生产级多租户、真实 CRM、在线 Judge 或跨进程 exactly-once。
