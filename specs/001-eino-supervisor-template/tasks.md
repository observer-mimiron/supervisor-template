---
description: "M9-M12 observability, MySQL runtime, local smoke, and Langfuse evaluation ledger"
---

# Tasks: M9-M12 Runtime Closure

本文件只记录本轮 M9-M12 的实施状态。M0-M8 的历史任务保留在 Git 历史中；本文件不把配置存在、计划或测试代码当作运行证据。

## M9: Observability and configuration

- [x] T001 补齐 observability 配置结构、启动校验、resource attributes、Langfuse endpoint/header 环境变量和文件观测字段；证据：`internal/config/config.go`、`internal/config/config_test.go`。
- [x] T002 接入 HTTP W3C trace context，返回 `X-Trace-ID`/`X-Request-ID`，并把请求关联 ID 投影到 SSE；证据：`internal/interfaces/http/tracing.go`、`internal/interfaces/http/handler_test.go`。
- [x] T003 接入结构化 JSON 日志、敏感字段/内部路径脱敏和有界文件轮转；证据：`internal/infrastructure/observability/logging.go` 及其合同测试。
- [x] T004 让 context-aware EventStore/Checkpoint 路径承接观测子 span context；证据：`internal/infrastructure/observability/observability.go` 及合同测试。

## M10: Database runtime

- [x] T010 提供 MySQL schema/seed 和 GORM adapter；证据：`internal/infrastructure/persistence/mysql/`、`internal/infrastructure/persistence/testdata/001_init.sql`。
- [x] T011 通过既有 `/api/chat` 注册订单查询和受审批保护的订单插入；证据：`config` 注册合同、`18096` 宿主机 Go 服务 smoke。
- [x] T012 验证批准后插入幂等，重复 resume 不新增订单；证据：`mysql-insert-smoke` 两次 resume 均返回同一 `order_id=3`，唯一 `completed`。

## M11: Runtime chain and dependency boundary

- [x] T014 Compose 只启动 MySQL，Go 服务由宿主机运行；MySQL 容器实际为 healthy，宿主机 Go 服务 `18096` health 返回 `ok`。
- [x] T015 修复并验证 fake 模拟触达真实 composition -> Application -> Worker -> Tool 链路；`18095` fake smoke 审批后成功，payload 为 audience projection。
- [x] T016 验证只读、两步串行、审批前无写入、reject、cancel、重复 resume 的 HTTP/SSE 合同；证据：`18095` smoke 和 `internal/composition/composition_test.go` 回归测试。

## M12: Langfuse evaluation

- [x] T020 在 M9-M11 运行链路收口后执行 Langfuse 多角度评测；真实请求、结构性评分、失败分类和凭证/网络前置条件见 `docs/langfuse-evaluation-20260928.md`。
- [x] T021 评测结果基于 Langfuse 中实际可检索的 trace/observation；配置存在和 exporter 初始化仅作为前置条件，不替代运行证据。

## Verification gate

- [x] T030 `go test ./...`
- [x] T031 `go test -race ./...`
- [x] T032 `go build ./cmd/server/`
- [x] T033 `go vet ./...`
- [x] T034 `git diff --check`

## Phase 13: Convergence

- [x] T035 建立 `eval/datasets/` 版本化 Case 数据集与严格 JSON schema/loader，覆盖正向、负向、边界和多样性样本，并拒绝未知字段、重复 key/ID、非法 timeout/retry 和动态预期，依据 plan: Phase 1（missing）。
- [x] T036 实现 `eval/runner/` 本地 HTTP/SSE Case Runner，覆盖 fixture 装配、认证、查询、审批、resume、cancel、总 deadline、pre-call retry、事件/Tool/状态证据与 cleanup，依据 plan: Phase 2（missing）。
- [x] T037 实现 `eval/evaluator/` 四维确定性评测、`cmd/eval` CLI 及 JSON/JSONL 报告，保留 pass/fail 原因、证据引用、版本和失败分类，依据 plan: Phase 3（missing）。
- [x] T038 增加变更风险、人工 disposition、failure taxonomy 与 Judge/rubric JSON 合同；默认只产生告警和人工 Review 输入，不改变本地硬门禁，依据 plan: Phase 4（missing）。
- [x] T039 增加最小 CI workflow 与 Go 包依赖方向检查，接入全量 Go 测试、评测包测试、Case Runner、风险分层和非零退出门禁，并明确 Required Checks/Reviewers 未配置前不得宣称已生效，依据 plan: Phase 5（missing）。
- [x] T040 为 Case/run 增加可选且低基数的 `case_id`、`case_version`、`code_version` 和 evaluator version OTLP 关联，并验证不写入用户输入、Prompt、报告或 Tool payload，依据 plan: Phase 6（partial）。
- [x] T041 完善运行时结构化日志、HTTP/Run/Model/Worker/Tool/Checkpoint/Event/MySQL trace 关联及低基数指标，覆盖请求量/延迟/错误、终态、调用延迟、重试、审批等待、恢复、预算耗尽和 exporter 失败，并补齐错误诊断字段合同，依据 FR-023/FR-024/FR-025/FR-026、SC-011/SC-012（partial）。
- [x] T042 将观测 exporter、日志和 Trace snapshot 故障接入一次性可检索 degraded signal，保持业务状态不变；接通 `TraceFile` 的脱敏、权限、大小/日期轮转与保留，并增加进程重启后的 snapshot 读取测试，依据 FR-027/FR-029/FR-031、SC-015（partial）。
- [x] T043 为 MySQL 查询和订单插入增加 GORM OpenTelemetry span 自动化合同测试，验证查询/插入 span 到达本地 provider 且不包含 SQL 参数或用户 payload，依据 FR-021/SC-012（partial）。
- [x] T044 补齐评测方法、Case 清单、报告字段、失败回流和本地 runner 验证说明，更新 quickstart 与边界文档，只记录实际执行的命令和证据，依据 plan: Phase 7（partial）。

## Phase 14: Convergence

- [x] T045 增加至少一个人工维护的 `diversity` Case，覆盖变更 subject 与执行路径，并让 loader/本地 Runner/确定性报告实际执行该版本；证据：`eval/datasets/synthetic-operations-v1.json`、`go run ./cmd/eval ... -risk high` 的 8 Case 通过结果。
- [x] T046 为本地 Runner 增加窄安全证据端口，记录 fake write count 和 cleanup result；side-effect evaluator 基于实际写入/清理证据断言审批前无写入、重复恢复至多一次；证据：高风险报告中只读/拒绝 `fake_write_count=0`、批准触达 `fake_write_count=1` 且 `cleanup_result=ok`。
- [x] T047 增加显式 PR risk/impact-tag 选择合同与 CLI/CI 接线：low/medium 运行基础及匹配标签 Case，high 或未知风险运行全量 Case，并保留 Required Checks/Reviewers 需平台配置的边界；证据：`eval/selection.go`、`cmd/eval`、`.github/workflows/ci.yml`，low + `audience-query` 实际 3 Case 通过。
- [x] T048 将 architecture evaluator 的硬编码 Tool allow-list 改为基于启动注册 Worker/Tool 快照及安全授权/Policy 证据的确定性检查，避免注册表变化导致评测误报；证据：`composition.RuntimeSnapshot`、Runner evidence 和 `TestArchitectureUsesRuntimeSnapshotAndPolicyEvidence`。
- [x] T049 在 Case 报告中增加结构化 `failed_assertion`、failure taxonomy 和 nullable human disposition 关联，保留现有独立 evaluator 结果与 Judge advisory 边界；证据：`eval/evaluator/evaluator.go`、`TestFailedAssertionsBecomeFailureRecords`。

## Phase 15: Convergence

- [x] T050 让 `business_correctness` evaluator 解析并断言 `ExpectedResults.Result` 的安全结构化字段（至少覆盖 count、customer_ids、spend_365d_total），同时保留不写入原始 Tool payload 的报告边界；`TestBusinessAssertsStructuredExpectedResult` 与本地高风险评测 8/8 通过。
- [x] T051 补齐运行期结构化日志、Application Run/Worker/Tool/审批/重试/预算 span 与低基数指标，贯通同一 HTTP trace context；`TestRuntimeObserverCorrelatesApplicationSpanAndRedactsLogFields` 验证 span 关联及脱敏，observability/application 合同测试通过。
- [x] T052 扩展 stability evaluator 与 Evidence，硬断言 `Retries <= Case.RetryBudget`、重复 Case 的确定性 verdict 和 step 级失败引用；Evaluator 覆盖预算/重复 verdict，Application 终态携带当前 step ID，相关回归测试通过。
- [x] T053 让 diversity Case 的 `preconditions.subject`、fixture/初始状态实际参与 Runner 装配和认证/证据采集；高风险报告记录 `demo-user-2` 与 `synthetic_audience_v1`。
- [x] T054 增加并实际运行代表性 negative/boundary Case，覆盖未知能力、非法输入和等待审批取消；版本化 Dataset 高风险运行 8/8 通过。

## Phase 16: Convergence

- [x] T055 Reconcile the plan's scope and status with the explicit M9-M12 runtime sequence and completed evidence, including MySQL/GORM delivery and post-runtime Langfuse evaluation per spec FR-019-FR-032 (plan: Scope Reconciliation / Not Included, partial); evidence: updated `plan.md` scope reconciliation, current limits, and reference status.

## Phase 17: Convergence

- [ ] T056 Propagate one trace ID across the `/api/chat`, approval, resume, cancel, Worker, Tool, Checkpoint, Event, and MySQL spans, and verify parent-child propagation per SC-011 (partial).
- [ ] T057 Add `service`, `env`, `trace_id`, and `span_id` to structured runtime logs while preserving redaction and bounded fields; assert the FR-023 field contract (partial).
- [ ] T058 Project `error_class`, `retry_decision`, and a safe error fingerprint into production error logs and Trace attributes; add FR-026 diagnostic contract coverage (partial).
- [ ] T059 Reconcile the remaining Summary, Post-design gate, and Phase 7 scope statements with the M9-M12 requirements and task ledger without claiming deferred Score API/Judge work (plan: scope declarations, partial).
