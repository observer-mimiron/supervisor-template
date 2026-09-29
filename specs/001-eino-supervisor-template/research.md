# Research Decisions: Evaluation-Driven Architecture Guardrails

## Reference project findings

`/home/huang/workspace/suanming-agent/eval` already provides versioned JSON datasets, repeatable `/api/chat` replay, SSE/trace assertions, timeout budgets, structured reports, failure categories, unit tests for the runner, and a split between default smoke, explicit online suites, and non-gating Judge review. Relevant local sources inspected:

- `eval/README.md`: evidence precedence, dataset conventions, gate levels and boundaries.
- `eval/datasets/runtime-smoke-v2.json`: dataset name/version, fixed Case IDs, positive contract assertions and optional setup turn.
- `eval/runner/run_langfuse_eval.py`: strict JSON duplicate-key rejection, one total deadline per Case, unique session IDs, stable failure classes, structured report metadata and CI exit code.
- `eval/runner/test_run_langfuse_eval.py`: loader, timeout, report, trace-exclusion and evaluator behavior tests.
- `eval/runner/run-agent-regression.sh` and `Makefile`: default small smoke vs explicit larger suites.

The reference runner is Python and requires Langfuse to locate and inspect traces; it also carries suanming-specific routes and answer assertions. Reuse its dataset/report/test patterns, not its Langfuse client or business implementation. Local reference facts are based on source as of 2026-09-28.

## Decision: Keep the dataset in versioned JSON

Use `eval/datasets/*.json` with top-level `name`, `version`, and `cases`, matching the parent project's structure and the repository's JSON conventions. Implement strict duplicate-key detection in the loader; preserve human-owned expected results and evaluator rules as read-only data.

**Rationale:** This provides a human-reviewable source of truth for Case standards and avoids introducing another parser/dependency.

**Alternatives considered:** YAML was rejected to avoid a new parser path; parent-project Python scripts were rejected because their runtime and Langfuse coupling do not match this Go service.

## Decision: Adapt to the actual HTTP/SSE contract

Use standard-library `net/http` against the existing router/handler test assembly. `/api/chat`, `/api/runs/{run_id}/approval`, `/resume`, and `/cancel` remain the only business endpoints. Parse existing SSE `EventEnvelope` fields and preserve `sequence`, `type`, `run_id`, `trace_id`, and safe data.

**Rationale:** This verifies authentication, application resource authorization, Policy Gate, Manager, Tool and public event projection together.

**Alternatives considered:** Calling `run.Service` directly would skip the public boundary. Copying the parent project's `/api/chat` request helper is insufficient because authentication, payload and event schema differ.

## Decision: Reuse scenario and timeout practices, but keep tests local

Use a unique `run_id`/conversation per Case, one total deadline across multi-step setup + request + approval/resume, explicit retry budget, and tests for loader/evaluator/report behavior. Default execution uses fake model/tools and does not require network credentials.

**Rationale:** The parent project documents real failures caused by shared sessions, setup-trace contamination, and per-request rather than total deadlines. The same reproducibility lessons apply without requiring its online services.

**Alternatives considered:** Per-step independent timeouts were rejected because a multi-step Case could exceed its declared total budget.

## Decision: Evidence collection must have a narrow local adapter

Existing SSE and `RunEvent` provide ordered event/tool-call/terminal evidence; HTTP supplies trace/request IDs. `FakeRegistry.OutreachCount()` proves the synthetic side effect count, but there is no generic Tool invocation ledger, and `composition.App` does not directly expose all stores/registry. Add only the smallest sanitized evidence hook/adapter needed for evaluator input; do not infer results from logs or scrape trace files.

**Rationale:** A Case report needs structured evidence while runtime state remains owned by the application. No SQL evidence is claimed when the default case has no database.

**Alternatives considered:** Treating trace/log presence as pass evidence was rejected; exposing all private runtime dependencies as public API was rejected.

## Decision: Deterministic evaluator first, report failures by assertion

Run four independent hard evaluators: business correctness, architecture boundary, side-effect safety, and stability. Reports carry dataset/Case version, code revision, evaluator versions, generated time, per-assertion result, stable failure class and evidence reference. Do not reduce the result to one score or include full response/tool payload by default.

**Rationale:** The parent runner demonstrates useful revision/dataset metadata, failure classification and exit-code behavior; this project also requires each failed assertion to be auditable.

**Alternatives considered:** A single score or Judge-only gate was rejected because it hides the failure cause and is not deterministic.

## Decision: Make static architecture checks and risk review explicit gates

The current checkout uses the repository-local `cmd/archcheck` command and CI workflow as the deterministic package-dependency check. Its output and failure behavior are verified alongside Go tests and the local Case Runner. Hosted Required Checks/Reviewers remain a repository-settings concern, not an implied property of the workflow file.

PR risk is human-declared and defaults to high when uncertain. Changes touching Policy, identity/resource authorization, approval, idempotency, state ownership, persistence, side effects, or dependency direction run the full dataset and require a recorded human approval. This approval is separate from failure calibration and from the advisory LLM Judge.

**Alternatives considered:** Relying on prose architecture rules or a Judge was rejected because neither reliably blocks a dependency violation; inferring risk from generated code was rejected because it could silently narrow the regression set.

## Decision: Optional Judge and feedback stay outside the hard gate

Define JSON contracts for Judge output and human disposition, but keep the default runner offline and the Judge disabled/fake. A human must classify a failure before it is promoted into a new Case, deterministic rule, or Rubric.

**Rationale:** The parent project itself separates its answer Judge from runtime contract checks; the current user requirement is stricter about no external prerequisites and human calibration.

**Alternatives considered:** Copying Langfuse score writes as a requirement was rejected because Langfuse outage or credentials must not block local contracts.

## Deferred boundaries

Do not add Apifox import, real CRM, MySQL/GORM SQL evidence, full Langfuse Dataset/Evaluator/Score, online LLM Judge, or cross-process exactly-once in this plan. Each needs a separate, code-backed contract and validation path.

## Durable execution research (2026-09-29)

本阶段只使用本地参考源码和本地 Go module cache；Temporal Go SDK 与 LangGraph 上游源码不在
当前工作区，因此它们只作为待验证的概念基线，不能作为本地实现证据。完整记录见
[`docs/research-durable-execution.md`](../../docs/research-durable-execution.md)。

| 参考 | 具体能力 | 当前选择 | 状态 |
|---|---|---|---|
| Eino module cache `v0.9.12` (`compose/checkpoint.go`, `internal/core/interrupt.go`, `adk/runner.go`) | 窄 CheckPointStore、checkpoint ID 和 Runner Resume | 保持 Eino 在 infrastructure；Application 独立保存 `ExecutionPlan` | `partial` |
| Coze Studio `fefb05ff` (`execute_history_store.go`) | 条件状态更新先认领中断执行 | 引入最小 Run lease 的原子 claim/check/release，不搬平台工作流 | `partial` |
| ecommerce sample `0c31ebb` (`app/main.py`, order repository/models) | thread/checkpoint 恢复和唯一幂等键 | 保留当前 PlanStep 幂等键与 checkpoint identity，不引入 Python/SQLAlchemy | `partial` |
| Temporal/LangGraph 上游 | 持久工作流、队列、pending writes 等成熟语义 | 本轮不抓取、不复制；只在边界中记录差距 | `deferred` |

### Durable design decisions

- `ExecutionPlan` 是 Run/Step 状态唯一 owner；Repository 负责快照，Checkpoint 是恢复游标，
  EventStore 是可重放投影。
- Run lease 使用随机 owner token 和 expiry。Memory 仅用于单进程合同测试；File 以每 Run
  的 OS `flock` 协调本地进程；MySQL 复用现有 GORM adapter 的条件 upsert。释放必须校验
  owner，过期租约才允许接管。
- 接管一个持久化为 `running` 的步骤时进入 `waiting_reconciliation`，不得把崩溃解释成
  Tool 未执行；unknown outcome 永不自动重试。
- Plan、Checkpoint 和 EventStore 不在同一事务时，只保证单存储原子性和 Resume 可修复的
  at-least-once 投影；不能宣称跨存储 exactly-once。事务 outbox、队列、lease renewal、
  跨主机共享文件和自动 reconciliation 均 `deferred`。
