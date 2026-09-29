---
description: "Actionable tasks for the enterprise durable-execution closure"
---

# Tasks: Enterprise Durable Execution Closure

**Input**: Design documents from `specs/001-eino-supervisor-template/`

**Scope**: Complete the remaining persistence, lease, crash-recovery, idempotency and event-projection
work. Existing HTTP/SSE, Supervisor routing, Policy Gate, WorkerRunner, approval, cancel, resume,
unknown-outcome branch, fake Tool, local Dataset/Runner/Evaluator and eight Case baseline are treated
as implemented evidence and are not repeated as new feature work.

**Constraints**: Keep `ExecutionPlan` as the only Run/Step owner; keep HTTP, Eino and GORM out of
Domain; preserve memory/file fake implementations; do not add a queue, DAG, outbox, generic
Component, ORM or new top-level directory; never claim cross-store or external exactly-once.

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish deterministic fixtures and evidence boundaries before changing execution code.

- [x] T001 [P] Record the current implemented/partial/deferred baseline and affected files in `PROGRESS.md` and `docs/current-capability-and-gap-report.md`, explicitly leaving cross-store exactly-once and production HA deferred.
- [x] T002 [P] Add a controllable clock, random owner-token helper and lease-duration fixture in `internal/application/run/durable_test_helpers_test.go` without changing production ownership or adding a new dependency.
- [x] T003 [P] Add fault-injecting Repository, CheckpointStore and EventStore test doubles in `internal/application/run/service_test.go` and `internal/application/run/durable_execution_test.go` for failures after Plan save, Checkpoint save and Event append.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Finish the shared persistence and lease contracts before any user-story integration.

**Checkpoint**: Memory/file/MySQL adapters expose the same claim semantics, persisted snapshots carry
`ErrorClass` and `UpdatedAt`, and EventStore tests prove sequence/terminal invariants.

- [x] T004 [P] Finalize `RunLease` and `RunLeaseStore` validation, owner-token matching, expiry comparison and release semantics in `internal/application/contracts.go` and `specs/001-eino-supervisor-template/contracts/runtime-foundation.md`.
- [x] T005 [P] Persist `ExecutionPlan.ErrorClass`, `ExecutionPlan.UpdatedAt`, `PlanStep.ErrorClass` and `PlanStep.UpdatedAt` through clone, JSON and load paths in `internal/domain/agent/contract.go`, `internal/infrastructure/persistence/memory.go` and `internal/infrastructure/persistence/file.go`.
- [x] T006 Complete conditional claim, ownership check and owner-checked release for memory, local-file and existing GORM/MySQL adapters in `internal/infrastructure/persistence/lease.go` and `internal/infrastructure/persistence/mysql/mysql.go`; retain `run_leases(run_id, owner_token, expires_at)` in `internal/infrastructure/persistence/testdata/001_init.sql`.
- [x] T007 [P] Add shared adapter contract tests in `internal/infrastructure/persistence/lease_test.go` proving two live owners cannot claim one Run, an expired lease can be claimed, a non-owner cannot release, and cancellation returns the context error.
- [x] T008 Wire the selected lease store into `application.Dependencies` in `internal/application/contracts.go` and `internal/composition/composition.go` using the existing memory/file storage choice, and fail health checks when the configured adapter is missing.
- [x] T009 [P] Add EventBus contract tests in `internal/infrastructure/eventbus/memory_test.go` and `internal/infrastructure/eventbus/file_test.go` proving sequences start at 1 and increase contiguously, duplicate EventID is idempotent, and a Run has at most one terminal event.

---

## Phase 3: User Story 1 - Durable Read-Only Execution (Priority: P1) 🎯 MVP

**Goal**: A read-only `/api/chat` Run persists its plan/step progress and can be resumed by one
owner without re-running a completed step.

**Independent Test**: Use the fake read-only Tool with two competing owners and a file-backed store;
after one owner completes a step, a new process/service instance resumes from the persisted Plan and
Checkpoint, emits monotonic events and performs no duplicate Tool call.

### Tests for User Story 1

- [x] T010 [P] [US1] Add a concurrent Start/Resume claim test in `internal/application/run/durable_execution_test.go` asserting exactly one owner succeeds while the other receives a resumable busy result and no duplicate read-only Tool call occurs.
- [x] T011 [P] [US1] Add a file-backed restart test in `internal/application/run/service_test.go` covering persisted Run/Plan/Step `attempt`, `ErrorClass`, `UpdatedAt`, terminal status, Checkpoint version and ordered events across two Service instances.

### Implementation for User Story 1

- [x] T012 [US1] Claim a `RunLease` with a fresh owner token before Start and Resume mutate a Run, check ownership around every Repository/Checkpoint/EventStore write, and release it on all exits in `internal/application/run/service.go`.
- [x] T013 [US1] Update `executeLocked` in `internal/application/run/service.go` to persist step attempt status, error classification and update time before/after WorkerRunner calls while preserving pre-call-only retry and existing budget limits.
- [x] T014 [US1] Make Resume derive missing Checkpoint/progress/text projections from the persisted `ExecutionPlan` under the lease in `internal/application/run/service.go`; preserve EventStore sequence checks and document that repair is at-least-once projection.

**Checkpoint**: US1 can run and recover independently using memory/file fake infrastructure; no
approval or external side-effect path is required for this checkpoint.

---

## Phase 4: User Story 2 - Approved Idempotent Side Effect (Priority: P2)

**Goal**: Approval, cancellation and repeated Resume are serialized by the Run lease and an approved
fake side effect executes at most once through the existing PlanStep idempotency key.

**Independent Test**: Submit the existing approval-required fake request, approve it, issue concurrent
or repeated Resume calls and verify `fake write count == 1`, one terminal event and the same persisted
result on every replay.

### Tests for User Story 2

- [x] T015 [P] [US2] Add an approval/repeated-Resume idempotency test in `internal/application/run/service_test.go` asserting duplicate Resume never re-invokes the approved fake Tool and returns the original result.
- [x] T016 [P] [US2] Add a concurrent Approve/Resume/Cancel lease test in `internal/application/run/durable_execution_test.go` asserting only the current owner mutates approval or terminal state and a losing owner cannot append a second terminal event.

### Implementation for User Story 2

- [x] T017 [US2] Wrap `Approve` and the state-mutating portion of `Cancel` with the same claim/check/release path used by Start/Resume in `internal/application/run/service.go`; preserve resource authorization and Policy Gate boundaries.
- [x] T018 [US2] Make approved side-effect completion persist the existing `PlanStep.IdempotencyKey`, attempt result and terminal classification before replay, and route uncertain commit errors to `waiting_reconciliation` in `internal/application/run/service.go`.
- [x] T019 [US2] Expose only sanitized fake write-count evidence through `internal/composition/composition.go` and `eval/runner/runner.go`, without adding a new business endpoint or writing full Tool payloads.

**Checkpoint**: US1 and US2 both pass independently; approval remains the only authority that
unlocks the side-effect Tool.

---

## Phase 5: User Story 3 - Crash Recovery and Reconciliation (Priority: P3)

**Goal**: Lease expiry and partial writes produce explicit recoverable states; in-flight unknown
outcomes are never automatically retried and terminal projections are repaired without duplication.

**Independent Test**: Inject process-exit-equivalent failures before Tool call, after Tool start,
after Plan save, after Checkpoint save and after Event append; let a second owner take over and verify
the expected reconciliation/projection behavior, event sequence and unique terminal.

### Tests for User Story 3

- [x] T020 [P] [US3] Add lease-expiry takeover tests in `internal/application/run/durable_execution_test.go` using two memory/file store instances and a controllable clock; assert the new owner can claim only after `expires_at <= now`.
- [x] T021 [P] [US3] Add unknown-outcome crash tests in `internal/application/run/service_test.go` proving a step persisted as `running` becomes `waiting_reconciliation`, returns `RUN_OUTCOME_UNKNOWN`, and never automatically retries the side effect.
- [x] T022 [P] [US3] Add partial-store failure tests in `internal/application/run/service_test.go` using the fault doubles from T003; cover Plan/checkpoint/event failures and assert the next Resume either repairs projections or leaves an explicit recoverable error.
- [x] T023 [P] [US3] Add terminal replay tests in `internal/application/run/service_test.go` proving repeated Resume repairs missing terminal/progress events, keeps sequences monotonic and never emits a second `completed`, `failed` or `canceled` event.

### Implementation for User Story 3

- [x] T024 [US3] On expired-lease takeover, inspect the persisted Plan and transition any `StepRunning`/`RunRunning` in-flight work to `StepWaitingReconciliation`/`RunWaitingReconciliation` before invoking a Worker in `internal/application/run/service.go`.
- [x] T025 [US3] Make all Start/Approve/Resume/Cancel defer paths release the owner lease and surface release/storage errors without converting them into successful business state in `internal/application/run/service.go`.
- [x] T026 [US3] Harden `repairStepProjectionsLocked` and `repairTerminalEventLocked` in `internal/application/run/service.go` to use persisted Plan status as authority, preserve EventStore idempotency and return a storage-classified recoverable error on partial failure.
- [x] T027 [US3] Add optional MySQL adapter/integration coverage for claim, expiry takeover and owner release in `internal/infrastructure/persistence/mysql/mysql_test.go`, reusing `internal/infrastructure/persistence/testdata/001_init.sql`; skip only when Compose/MySQL is unavailable and record that environment boundary.

**Checkpoint**: Unknown external outcomes require reconciliation; recovery never infers that a
side-effect Tool did not run merely because the process exited.

---

## Phase 6: Polish & Cross-Cutting Verification

**Purpose**: Reconcile evidence labels, documentation and the full required verification commands.

- [x] T028 [P] Update `docs/architecture.md` and `docs/current-capability-and-gap-report.md` with Run lease ownership, per-store atomicity and the explicit no-exactly-once boundary.
- [x] T029 [P] Update `docs/research-durable-execution.md`, `specs/001-eino-supervisor-template/research.md`, `specs/001-eino-supervisor-template/data-model.md` and `specs/001-eino-supervisor-template/quickstart.md` with implementation evidence and remaining `partial`/`deferred` labels.
- [x] T030 [P] Extend `eval/evaluator/evaluator.go`, `cmd/eval/main.go` and `docs/evaluation-method.md` only as needed to report Case total, passed/failed, each Case terminal and fake write count without storing sensitive payloads.
- [x] T031 Run and record `go test ./...`, `go test -race ./...`, `go build ./cmd/server/`, `go vet ./...` and `go run ./cmd/archcheck` in `PROGRESS.md`; do not mark MySQL recovery complete without an actual adapter/integration result.
- [x] T032 Run `go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v1.json -report ./tmp/eval-report-enterprise.json -code-version enterprise-execution-v1 -config ./config.example.toml` and record all eight Case terminals and fake write counts in `PROGRESS.md`.
- [x] T033 Run `git diff --check` and audit changed files for dependency-direction violations, secret/payload leakage and unsupported exactly-once claims in `docs/architecture.md`, `docs/current-capability-and-gap-report.md` and `PROGRESS.md`.
- [x] T034 [P] Expose registered Tool risk, approval and idempotency metadata in `internal/composition/composition.go` and `eval/runner/runner.go` so evaluators do not identify side effects by business Tool ID.
- [x] T035 [P] Enforce the four hard evaluator dimensions plus the mandatory evidence matrix during dataset loading and case evaluation in `eval/case.go`, `eval/loader.go` and `eval/evaluator/evaluator.go`; missing evidence is a hard failure.
- [x] T036 [P] Add adversarial evaluator contract tests for missing dimensions/evidence, forged terminal fields, duplicate EventID, RunID/trace mismatch, non-monotonic sequences, duplicate terminal events, policy-before-tool ordering and generic side-effect metadata in `eval/evaluator/evaluator_test.go` and `eval/loader_test.go`.
- [x] T037 [P] Add an optional `MYSQL_TEST_DSN` lease integration test in `internal/infrastructure/persistence/mysql/mysql_test.go`; skip when the local Compose/MySQL environment is unavailable and keep that boundary documented.
- [x] T038 Run the enterprise Case command again with `./tmp/eval-report-enterprise-v2.json`, record its 8-case summary and run `git diff --check` after the evaluator contract changes.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 Setup**: No code dependency; establishes test clocks and fault doubles.
- **Phase 2 Foundational**: Depends on Setup and blocks all user-story implementation.
- **US1 P1**: Depends on Phase 2; delivers the MVP read-only durable Run path.
- **US2 P2**: Depends on Phase 2 and reuses US1 lease/persistence helpers; its fake side-effect test is independently runnable.
- **US3 P3**: Depends on Phase 2 and the state transitions exercised by US1/US2; adapter and fault-injection tests can start in parallel with US1/US2.
- **Phase 6 Polish**: Depends on the desired story checkpoints and actual verification results.

### User Story Dependencies

- **US1**: Foundational only; no approval or external side-effect dependency.
- **US2**: Foundational plus the existing PlanStep/idempotency contract; does not require US3 recovery behavior.
- **US3**: Uses the persisted statuses and event projection paths from US1/US2, then adds takeover and partial-write behavior.

Evaluation guardrails in T034-T036 depend on the existing Runner evidence contract; T037 is adapter-level evidence only and does not make production MySQL recovery `implemented`.

## Parallel Execution Examples

### Foundational

```text
T004 -> application lease contract and runtime-foundation contract
T005 -> Plan/Step persistence fields and clones
T009 -> memory/file EventBus sequence and terminal tests
```

After T004-T006, T007 and T008 can proceed in parallel because they touch adapter tests and
composition wiring respectively.

### User Story 1

```text
T010 -> concurrent claim test
T011 -> file-backed restart test
```

Once both tests exist, T012-T014 must run sequentially because they share the Service state owner.

### User Story 2

```text
T015 -> repeated Resume side-effect test
T016 -> approval/control lease test
```

T017 and T018 then update the same application boundary; T019 can proceed in parallel once the
composition evidence hook is defined.

### User Story 3

```text
T020 -> lease expiry takeover
T021 -> unknown outcome recovery
T022 -> partial projection failure
T023 -> terminal replay
```

These tests use separate files and fixtures. T024-T026 are sequential Service changes; T027 can run
in parallel when a local MySQL/Compose service is available.

### Evaluation Guardrails

```text
T034 -> RuntimeSnapshot/Runner metadata
T035 -> loader and evaluator evidence contracts
T036 -> adversarial evaluator tests
T037 -> optional MySQL lease integration test
T038 -> final enterprise evaluation and diff audit
```

## Implementation Strategy

### MVP First (US1 only)

1. Complete Phase 1 and Phase 2.
2. Implement US1 lease-aware read-only Start/Resume and file-backed restart proof.
3. Stop and validate the existing `/api/chat` read-only Case plus `go test ./internal/application/run`.

### Incremental Delivery

1. Add US2 approval/idempotency and prove fake write count remains one.
2. Add US3 expiry takeover, unknown-outcome and partial-projection recovery.
3. Run the eight existing evaluation Cases and the full verification commands.
4. Update evidence labels only from actual test/adapter results.

## Notes

- Every task is unchecked until implemented and verified; the previous M9-M17 completed ledger is
  represented by the baseline statement above and `PROGRESS.md`.
- `[P]` means different files and no dependency on incomplete tasks in the same phase.
- MySQL lease integration is optional for the current single-instance target; real multi-instance recovery remains deferred until a deployment requires it. The GORM adapter itself does not provide the Runtime Repository/Checkpoint/EventStore backend.
- T002 and T016 are complete; the shared clock/owner fixtures and concurrent Approve/Resume/Cancel service coverage remain test-only and do not change production ownership.
- No task introduces a new top-level directory or claims external exactly-once side effects.

## Phase 7: Convergence

- [x] T039 [P] [US2] Bind every recorded fake write to a registered side-effect Tool call with matching step/worker/tool evidence, and reject writes accompanied only by unrelated read-only calls per FR-004/FR-005 and SC-002 (`implemented`).
- [x] T040 [P] [US2] Require approval evidence to match the complete `step_id` + `worker_id` + `tool_id` route in `eval/evaluator/evaluator.go`, and add same-step/different-tool adversarial coverage in `eval/evaluator/evaluator_test.go` per FR-004/FR-010 and SC-004 (`implemented`).
- [x] T041 [P] [US2] Reject duplicate side-effect `tool_call` evidence for the same approved step/idempotency route and add a generic repeated-call contract test in `eval/evaluator/evaluator_test.go` per FR-005/FR-006 and SC-003 (`implemented`).
