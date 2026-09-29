# Implementation Plan: Evaluation-Driven Architecture Guardrails

**Branch**: `001-eino-supervisor-template` | **Date**: 2026-09-28
**Spec**: [spec.md](./spec.md)
**Input**: user requirements in `已粘贴的文本.txt`

## Summary

把现有示例改造成可重复运行的本地评测闭环：沿用 `/home/huang/workspace/suanming-agent/eval` 已验证的版本化数据集、真实 API 回放、稳定断言、机器报告和失败分类；针对本项目 `/api/chat`、审批、resume、cancel 和 SSE 合同做轻量适配，采集运行证据并由确定性 Evaluator 输出分项结果。每次 PR 运行静态架构检查、Go 合同测试和本地 Case；硬门禁失败阻断合并。按变更风险选择 Case，高风险变更额外要求人工终审。第一阶段只使用 fake/内存和本地 HTTP，不复制父项目 Langfuse 依赖，不接 Apifox、真实 CRM、真实 MySQL 或在线 Langfuse；LLM Judge 只告警，人工校准和失败回流采用显式记录及版本化流程。

## Technical Context

**Language/Version**: Go 1.25
**Primary Dependencies**: 现有 Gin、Eino、OpenTelemetry 和 Go 标准库；不新增依赖
**Storage**: 仓库 JSON Case；运行报告 JSON/JSONL；测试数据和 Tool 状态使用现有 fake/内存实现
**Testing**: `go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`go vet ./...`、`git diff --check`，另加 `go run ./cmd/eval` 的本地 Case smoke
**Target Platform**: Linux/local development；CI 可无网络、无凭证运行
**Project Type**: Go HTTP/SSE Agent service with a local evaluation CLI
**Performance Goals**: 默认 Case 集在本地 fake 环境可重复完成；单 Case 受自身 timeout/retry_budget 限制，Runner 不引入无界等待
**Constraints**: Case 预期和 evaluator 规则只读加载；AI 生成的请求步骤不能改验收标准；副作用仍必须经过 Policy Gate、Approval、Idempotency、Tool、Audit/Event；观测、Judge 和报告失败不得改变业务终态；合并门禁只接受确定性结果，Judge 不阻断；高风险人工审批是额外放行条件
**Scale/Scope**: 首批至少四类 Case（正向、负向、边界、多样性），覆盖单 Worker、两步串行、审批触达、授权/注册/输入拒绝、取消/超时/恢复和幂等重复；不实现通用 DAG、跨进程 exactly-once 或平台化 Dataset 服务

### Phase 18 technical context

**Durable state**: `ExecutionPlan` in `Repository` is the Run/Step authority; `CheckpointStore`
is a monotonic recovery cursor and `EventStore` is an ordered projection. Existing memory/file
implementations remain available; file writes are per-store atomic JSON replacement, not a shared
transaction.

**Lease**: Application owns `RunLeaseStore` (`run_id`, random `owner_token`, `expires_at`,
claim/check/release). Memory is process-local; File uses a per-Run OS lock plus an atomically
replaced lease record; MySQL reuses the existing GORM adapter. No queue, outbox, DAG or new ORM.

**Recovery boundary**: An expired lease may be reclaimed. A persisted `running` step is treated as
`waiting_reconciliation`, never as a safe-to-retry Tool call. Plan/checkpoint/event partial failure
is repaired by Resume from the Plan and is explicitly not cross-store exactly-once.

**Evidence status**: event ordering, unique terminal projection, file atomic replacement and the
unknown-outcome branch are `implemented`; lease adapters and Application integration are `partial`
until contract tests pass; distributed workflow service, lease renewal and external exactly-once
are `deferred`.

## Constitution Check

**Initial gate: PASS**

- 复用 `interfaces -> application -> domain`，Runner 只调用现有 HTTP/SSE 适配和应用合同，不把评测逻辑放入 Domain。
- Case、Runner、Evaluator、报告和回流记录属于 `eval/` 工具边界；它们不拥有运行状态、权限、审批、幂等或终态。
- 不新增 Worker/Tool 运行时注册，不绕过 Policy Gate、Approval、Idempotency、Audit/Event。
- 默认仅使用 fake/内存和本地替身，不要求真实模型、外部服务、凭证或 Docker。
- Trace、Tool 调用和事件只是证据；通过确定性规则产生结论，LLM Judge 只能产生软评审。
- 自动门禁至少运行 Go 测试、静态依赖方向检查和本地确定性 Case；任一硬门禁失败均非通过。当前仓库没有 CI workflow 或架构检查器配置，CI 接线属于本计划新增交付，不是现状。
- Case 运行及硬门禁通过不替代高风险人工终审；风险等级、评审人和结论必须留痕，未获人工批准的高风险变更不得放行。代码托管平台的必需审批保护需作为启用门禁的仓库配置前置条件。
- 失败只有在人工 disposition 后才能更新 Case、Evaluator 规则或 Rubric；更新后的版本必须进入下一次关联回归。
- 不修改宪法，不宣称当前已有的 OTLP/Langfuse 或 MySQL 配置就是完整评测能力。

**Post-design gate: PASS**

- `eval/` 是宪法允许的顶层边界；`cmd/eval` 只装配 CLI，不承载业务判断。
- Case 的人工字段与运行时证据分离；Runner 不能覆盖 `expected_results`、`forbidden_side_effects` 或 `evaluator_rules`。
- 报告保留 `case_id`、版本、`run_id`、`code_version` 和 evidence reference；敏感数据、凭证、完整 Prompt/Tool payload、内部路径不写入报告。
- MySQL、在线 Langfuse、真实 Judge 和 Apifox 明确标为 deferred/optional，不作为本地门禁。
- 高风险变更的人工审批与失败样本 disposition 分开建模；前者是合并条件，后者是评测规则/Case 校准流程。

## Existing Components To Reuse

- `internal/interfaces/http/handler.go`：已有 `/api/chat`、approval、resume、cancel 和稳定公开错误边界。
- `internal/application/run.Service`：已有计划、审批、恢复、取消、终态和幂等 owner。
- `internal/domain/agent.RunEvent` 与现有 event store：作为事件顺序和终态证据来源。
- `internal/infrastructure/examplebusiness`、`internal/infrastructure/tool/FakeRegistry`：固定匿名 fixture、两步汇总、模拟触达和 `OutreachCount`。
- `internal/infrastructure/observability`：已有 OTel/context 关联；评测读取 SSE trace ID 和安全结构化字段，不复制 Trace 总线。
- `internal/interfaces/http/handler_test.go`：复用其中真实路由装配、SSE 事件断言和审批/resume/cancel 场景；抽取最小共享测试装配，避免复制完整 fixture。
- 上级项目 `eval/datasets/*.json`、`eval/runner/run_langfuse_eval.py`、`test_run_langfuse_eval.py`、`run-agent-regression.sh`：借用数据集版本、严格 JSON、case timeout、唯一会话、报告元数据/失败分类和测试分层；其业务断言不适用于本项目。

## Scope Reconciliation

`spec.md` FR-019-FR-032 和 `tasks.md` M9-M12 是当前运行时交付验收范围：先完成可观测性、MySQL/GORM 与宿主机 Go 服务的运行链路，再在 M9-M11 收口后单独执行 Langfuse 多角度 trace 评测。本计划中的 Phase 1-7 补充本地确定性 Case、评测报告和 CI 门禁，不取代上述运行时任务。Langfuse 评测证据单独记录在 [langfuse-evaluation-20260928.md](../../docs/langfuse-evaluation-20260928.md)；配置或 exporter 初始化不算评测完成。

## Delivery Phases

### Phase 0: Baseline and research

确认当前 `PROGRESS.md` 中 M0-M8、合成业务、观测和未完成范围；固定 JSON 格式、报告字段、版本策略和本地 HTTP 执行方式。核验 `go-arch-lint` 对当前 Go 包边界的表达能力及固定版本运行方式；本地验证后将选定的静态检查命令固化到 CI。任何无法由代码/测试证明的能力保持 `partial` 或 `deferred`。

### Phase 1: Case dataset and schema

新增 `eval/datasets/` 下的人工维护 JSON 数据集，沿用父项目 `name`、`version`、`cases` 组织方式；Case 至少提供正向、负向、边界、多样性四类样本。加载器拒绝重复 JSON key、未知字段、缺失必填字段、重复 ID/版本、非法 timeout/retry 和动态预期。Case 结构见 [contracts/evaluation.md](./contracts/evaluation.md)。

### Phase 2: Minimal local Case Runner

在 `eval/runner/` 实现固定 fixture 准备、`httptest` 本地服务装配、SSE 请求解析、审批/resume/cancel 步骤、单 Case 总 deadline、pre-call retry 预算、事件/Tool/状态证据收集、cleanup 和 CI 退出码。沿用父项目对 setup 与正式请求共用总预算、每个 Case 使用唯一 run/session、排除前置请求证据的经验。Runner 只执行 Case 声明的步骤，不推断或修改验收标准；优先抽取现有 handler 测试装配函数。

### Phase 3: Deterministic Evaluator and report

在 `eval/evaluator/` 实现 `business_correctness`、`architecture_boundary`、`side_effect_safety`、`stability` 四个 evaluator。架构边界分别检查运行时允许的 Worker/Tool、Policy Gate/审批/幂等证据，以及静态 Go 包依赖方向；不从 Trace 推断静态结构。每个断言输出 pass/fail、原因、证据引用和 evaluator version；失败项完整保留，不压缩成单一总分。报告写 JSON，并可追加 JSONL 供 CI/回放，沿用父项目 dataset version、code revision、generated_at、通过/失败统计和分类汇总字段；默认不保存完整 response/Tool payload。

Case 带人工维护的影响范围标签。普通变更运行基础 Go 合同测试及其声明影响范围的 Case；风险高或涉及 Policy、权限、审批、幂等、状态所有权、持久化/副作用边界的变更运行全部 Case。影响范围由 PR 显式声明，不由模型推断；缺失或无法判定时按高风险处理。任何硬断言失败使评测命令和 CI 检查失败。

### Phase 4: Calibration and optional soft review contracts

新增变更风险记录、人工终审 disposition/failure taxonomy 与 rubric/Judge JSON 合同，默认不调用模型、不阻断退出码。高风险 PR 必须有人工 reviewer、结论和理由；拒绝/未处理不得放行。该审批与评测失败校准分开记录。LLM Judge 输入只能是脱敏 diff、Case、证据摘要和硬评测结果；输出必须带 model、prompt/rubric version、digests、confidence 和 `requires_human_review`，只产生告警和人工 Review 输入。失败样本只有人工确认后才生成新 Case、规则或 Rubric；改动后的 Case/规则必须在下一轮关联回归中实际执行并记录版本。

### Phase 5: CI merge gates

新增最小 CI workflow，固定 Go/tool 版本并在 PR 上运行：架构依赖检查、`go test ./...`、评测包测试及 Case Runner。硬门禁的非零退出码阻断合并；高风险 PR 运行全量 Case 且需要托管平台的必需人工审批保护。普通 PR 至少运行基础 Case 和显式影响标签对应的 Case。CI 不访问真实模型、数据库、CRM、Apifox 或在线 Langfuse。分支保护的 Required Checks/Required Reviewers 是仓库设置项，未配置前不能宣称门禁实际生效。

### Phase 6: Optional OTLP/Langfuse correlation

复用现有 OpenTelemetry/Langfuse exporter，为 Case/run 增加低基数的 `case_id`、`case_version`、`code_version` 和 evaluator version 关联，避免把用户输入或完整报告作为 span 属性。Local JSON 报告仍是验收事实来源；在线 Langfuse 未配置/不可用不得影响 runner 退出码。Langfuse Dataset/Score/feedback 的直接 API 双向同步不在本阶段承诺，只有具备稳定合同后才扩展。

### Phase 7: Documentation and verification

补充评测方法说明、运行说明、Case 清单和当前边界；只把实际运行的命令写回 `PROGRESS.md`。在线 Langfuse/Apifox/MySQL 作为后续独立 Spec/Plan，不在本计划中伪造完成状态。

## Not Included / Current Limits

- 本地 Case dataset、HTTP Runner、确定性 Evaluator、风险分层、CI workflow 和架构检查已实现；托管平台 Required Checks/Reviewers 仍需仓库设置，未配置前不宣称远端合并保护已生效。
- MySQL/GORM、三表示例业务、宿主机 Go + Compose 依赖链路及查询/审批插入 smoke 已实现；真实 CRM、营销渠道和真实外部写入仍未接入。
- OTLP/Langfuse exporter 与 M9-M11 之后的多角度 trace 评测已完成，结构证据和结果见独立报告。该评测不是在线 LLM 质量/Judge 评估；Langfuse Score/feedback 双向 API 同步仍不在范围内。
- Judge、人工终审与失败回流目前是显式合同和本地硬评测之外的流程输入；Judge 只能 advisory，不改变确定性门禁。
- 真实模型驱动的全部多步 Worker ToolCall、跨进程 exactly-once、真实 CRM 写入和生产级高可用观测仍未实现，不能由本地 Case Runner 或 trace 评测代替证明。

## Design Artifacts

- [research.md](./research.md)：父项目评测实践、可复用模式和本地差异。
- [data-model.md](./data-model.md)：Dataset、Case、证据、分项报告和人工校准模型。
- [contracts/evaluation.md](./contracts/evaluation.md)：JSON、Runner、Evaluator、Judge/feedback 和脱敏合同。
- [quickstart.md](./quickstart.md)：本地执行命令、预期报告和失败回流步骤（实现后验证）。

## Project Structure

```text
eval/
├── datasets/
│   └── synthetic-operations-v1.json # 四类人工验收 Case
├── case.go              # Case、步骤、预期和规则的版本化合同
├── loader.go            # 严格 JSON 加载和静态校验
├── runner/              # HTTP/SSE 执行、证据采集、cleanup、退出码
└── evaluator/           # 确定性 evaluator、分项结果和报告

cmd/eval/main.go          # 本地 runner CLI；只负责参数、装配和退出码
docs/evaluation-method.md # 方法、门禁分工、人工校准和已知边界
specs/001-eino-supervisor-template/contracts/evaluation.md
```

实现时若现有 handler 没有可复用的装配函数，只增加一个测试/本地装配入口；不新增业务 HTTP endpoint，不把 `eval` 依赖倒灌进 `internal/domain` 或 `internal/application/run`。

## Verification Order

1. `go test ./eval/...`：Case schema、loader、各 evaluator 和报告脱敏。
2. `go test ./...`、`go test -race ./...`：确保评测适配没有改变原运行合同。
3. `go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v1.json -report ./tmp/eval-report.json`：跑本地 Case，期望零硬评测失败且报告包含事件/Tool/终态证据。
4. 运行静态依赖检查和本地 Case CI 命令；分别验证硬门禁失败时退出非零、高风险变更要求全量 Case 且无人工批准时不得放行。
5. `go run ./cmd/server/ -f ./config.example.toml` 配合现有 quickstart：真实 `/healthz`、正向查询、未审批拒绝、审批 resume、重复 resume、cancel、timeout 和失败报告 smoke。
6. `go build ./cmd/server/`、`go vet ./...`、`git diff --check`。

## Complexity Tracking

| Decision | Reason | Simpler alternative rejected because |
|---|---|---|
| `eval/runner` 通过本地 HTTP/SSE 跑 Case | 验证真实用户链路和公开事件合同 | 只调用内部 Service 会漏掉认证、SSE 和公开错误边界 |
| JSON 报告保留分项结果和 evidence reference | CI 需要可定位失败，不能只有总分 | 单一总分无法区分业务、边界和架构失败 |
| Judge/人工回流只做合同 | 保留方法论扩展点但不引入在线依赖 | 直接接在线模型会让本地门禁不稳定且不能替代硬校验 |
| 高风险人工审批由 PR 风险声明和托管平台保护执行 | 不把人工审批伪装成可由本地 evaluator 自动判定的结论 | 自建审批服务会扩大边界；仓库保护必须在托管平台显式启用 |

## Reference Baseline

| Source | Reused pattern | Reference status | Local status / boundary |
|---|---|---|---|
| `../eval/README.md`, `../eval/datasets/runtime-smoke-v2.json` | 版本化 JSON dataset、稳定合同断言、case taxonomy | `implemented` in parent project | `partial`: 借鉴格式；父项目八字字段不复用，本地案例由人工维护 |
| `../eval/runner/run_langfuse_eval.py`, `test_run_langfuse_eval.py` | 严格 JSON、单 Case 总 deadline、唯一 session、SSE/trace assertions、结构化失败报告与测试 | `implemented` in parent project | `implemented`: 本地 Go loader/runner/evaluator 复用通用回放模式；不复制 Python/Langfuse client、在线 Score 或凭证依赖 |
| `../eval/runner/run-agent-regression.sh`, `../Makefile` | 小型默认 smoke 与显式扩展评测分层 | `implemented` in parent project | `partial`: 只借用 gate 分层，不复制服务启动、模型调用或 Langfuse availability gate |
| 本项目 `handler_test.go`、`RunEvent`、`FakeRegistry.OutreachCount` | 真实 HTTP/SSE、审批与终态证据 | `implemented` as local evaluation evidence | `implemented`: Runner 采集证据，四维 Evaluator 输出可定位报告 |
| 本项目架构文档/宪法 | 目录责任、依赖方向、启动期 fail-closed 原则 | `documented` | `implemented`: `cmd/archcheck` 和 CI workflow 已接入；托管平台 Required Checks/Reviewers 需另行配置 |

参考项目实现状态以 2026-09-28 本地源码为准；本项目评测 Runner、Evaluator 和报告的完成证据见 `tasks.md` Phase 13-15 与 `PROGRESS.md`，不把父项目的在线 Langfuse client 或 Score API 能力算作本项目实现。

## Phase 18: Durable execution closure (2026-09-29)

### Scope and decisions

- Reuse Application/Manager `ExecutionPlan` as the authoritative Run/Step snapshot. Persist status, attempt, terminal code/classification and update time through the existing Repository; keep Checkpoint as a recovery cursor and EventStore as a replayable projection.
- Add an Application-owned Run lease contract with random owner token, expiry, claim, ownership check and release. File mode uses a per-run OS file lock around an atomically replaced lease record; configured MySQL reuses the existing GORM adapter and a conditional upsert. Memory mode remains process-local. MySQL is optional and does not replace the memory/file Runtime Repository, Checkpoint or EventStore.
- Serialize one Run's Start/Approve/Resume/Cancel mutations with that lease. When an expired lease is reclaimed and the persisted step is `running`, move it to `waiting_reconciliation`; never infer that the external call did not happen.
- Preserve separate Repository, Checkpoint and EventStore writes. Resume repairs stale checkpoint and missing event projections from the Plan. This is recoverable at-least-once projection, not a cross-store transaction or exactly-once guarantee.
- Keep the current Tool idempotency key on the persisted PlanStep. Do not add an outbox, generic workflow engine, queue, DAG, database abstraction, or new runtime dependency.

### Reference comparison

| Local source | Relevant pattern | Decision/status |
|---|---|---|
| CloudWeGo Eino module cache `v0.9.12`, `compose/checkpoint.go`, `internal/core/interrupt.go`, `adk/runner.go` | CheckPointStore is a narrow byte store; Runner Resume loads by checkpoint ID. | `partial`: keep Eino behind WorkerRunner and persist the application-owned Plan separately. |
| Coze Studio local checkout `fefb05ff`, `domain/workflow/internal/repo/execute_history_store.go` | Conditional status update claims an interrupted execution before resume. | `implemented`: the principle is wired as the application-owned Run lease; platform workflow/queue behavior is not copied. |
| Local LangGraph-based ecommerce sample `0c31ebb`, `app/main.py`, `app/order/repository.py`, `app/order/models.py` | SQLite checkpointer resumes by thread ID; database idempotency key is unique and checked in the order transaction. | `partial`: reuse checkpoint identity/idempotency principles, not its Python graph or SQLAlchemy stack. |
| Temporal Go SDK and LangGraph upstream source | Not present in local checkouts/module cache. | `deferred`: this task is constrained to local references; no claim is made about their current implementation details. |

### Boundaries and verification

- File repository/checkpoint/event writes remain atomic per file, not a single transaction. The file lease coordinates cooperating application instances on a local filesystem; it is not a distributed lock for arbitrary network filesystems.
- MySQL lease uses the existing GORM adapter and requires the included `run_leases` schema. Adapter code and schema are present, but real MySQL multi-instance behavior has not been verified. Multi-instance deployment is not a current requirement, so that integration run is `deferred`, not a blocker for the single-instance memory/file target.
- `go test`, race, build, vet, archcheck and the existing eight local evaluation Cases remain required. Production HA, lease renewal for unbounded work, cross-host shared-file semantics, transactional outbox, exactly-once external side effects and automatic reconciliation are deferred.

### Implementation sequence

1. **Contract and snapshot fields**: keep `ExecutionPlan` as the owner; persist `ErrorClass` and
   `UpdatedAt` for the plan and each step. Add only the `RunLeaseStore` application port already
   planned; do not expose file locks or GORM types above infrastructure.
2. **Adapters**: require shared-contract tests for memory and local file `flock`; file tests use two
   store instances over one directory. Keep the GORM/MySQL conditional-claim integration test
   optional and run it only when a deployment actually selects MySQL leases.
3. **Application integration**: claim a lease for Start/Approve/Resume/Cancel and release it on
   every exit path. Check ownership around state writes. On expired takeover, convert durable
   `running` work to reconciliation before any Worker call.
4. **Projection repair**: make Resume derive missing progress/text/terminal projections from Plan;
   event append must remain idempotent and sequence-checked. A projection error is recoverable
   infrastructure failure, not a successful terminal result.
5. **Failure-injection contracts**: inject failures after Tool start, after Plan save, after
   Checkpoint save and after Event append. Assert no duplicate side effect, no second terminal,
   monotonic event sequence and explicit next action (`resume`, reconciliation or fail).
6. **Regression gate**: run the existing eight evaluation Cases plus the full Go/race/build/vet/
   archcheck/diff command set. Report MySQL and multi-process evidence separately from memory/file
   contract evidence.

### Post-design constitution gate: PASS with explicit limits

- Domain remains free of HTTP, Eino, GORM and file-lock types; `RunLease` is an application port and
  adapters remain under `internal/infrastructure`.
- `ExecutionPlan` remains the only Run/Step owner. Checkpoint and EventStore are projections/cursors,
  not alternate state machines. Supervisor/Worker/Tool still cannot choose authority or terminal state.
- Lease serialization narrows concurrent mutation but does not change Policy Gate, approval,
  idempotency or Final Guard responsibilities. No new top-level directory, queue, DAG or ORM is added.
- The design claims per-store atomicity and recoverable projection only. It explicitly does not claim
  cross-store transactions, external exactly-once, cross-host file locking or automatic resolution of
  unknown external outcomes.

### Planned artifacts and evidence labels

- `docs/research-durable-execution.md`: local reference comparison and implemented/partial/deferred
  boundary labels (`implemented` for memory/file lease and application recovery contracts; `partial`
  for cross-store atomicity and production MySQL; `deferred` for service-side workflow guarantees).
- [`data-model.md`](./data-model.md): Run/Step/Lease/Checkpoint/Event fields and partial-write matrix.
- [`contracts/runtime-foundation.md`](./contracts/runtime-foundation.md): lease and recovery contracts.
- [`quickstart.md`](./quickstart.md): contract and required gate commands; Phase 18 memory/file
  commands have been run. Real MySQL multi-instance recovery remains optional/deferred because it
  is not part of the current single-instance target.
