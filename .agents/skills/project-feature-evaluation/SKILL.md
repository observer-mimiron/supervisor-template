---
name: project-feature-evaluation
description: Turn a code change into evidence-backed acceptance checks using this repository's requirements, tests, architecture checks, and runtime cases.
metadata:
  short-description: Verify feature acceptance with project evidence
---

# Project Feature Evaluation

Use this skill while implementing a feature or when asked to assess whether a
change meets its requirements without violating this repository's architecture.

The goal is not "the tests passed". It is: **the claim is verified, and we can
show what would have happened if it were not.**

## Workflow

1. **Read before proposing.** Read the request, the relevant spec,
   `.specify/memory/constitution.md`, `docs/architecture.md`, `PROGRESS.md`, and
   the current diff. Treat the constitution as protected. If the expected
   behavior is ambiguous, name the ambiguity instead of deriving the requirement
   from the implementation.
2. **Turn the request into 1–3 Cases and have them reviewed.** A Case declares
   request steps, expected terminal/events/JSON fields, forbidden side effects,
   evidence sources, timeout/retry, and cleanup. A generated expectation is a
   **proposal, not truth** — a Case written from the same misunderstanding as
   the implementation passes while both are wrong.
3. **Decide which layer verifies what** (see below). Do not force everything
   into one layer.
4. **Run the gate and read the evidence.** Smallest relevant check first, then
   the whole gate.
5. **On failure, produce a triage, not just a report.** Fix, then add a Case
   that fails without the fix.
6. **Compare against the baseline** so "fixed one, broke another" cannot pass.
7. **Report status truthfully** using the vocabulary below.

## The layers, and who may block

| Layer | Verifies | Decided by | May block a merge |
| --- | --- | --- | --- |
| **L0** | formatting, static checks, dependency direction, unit/contract tests, `-race` | toolchain | **yes** |
| **L1** | runtime behavior through the real entrypoint (HTTP/SSE, persistence, side effects) | deterministic evaluators | **yes** |
| **L2** | semantic quality the deterministic checks cannot decide | LLM judge (Langfuse) | **no** — visibility only |
| **L3** | sampled human review, judge calibration | a human | **no** |

The rule that keeps this honest: **L2 can never turn an L1 red into a green.**
A model verdict is advisory; permissions, approvals, idempotency, terminal
state, and side-effect counts are decided by deterministic code.

If you are adding an LLM judge, calibrate it against human labels first and
record the sample size and agreement rate. An uncalibrated judge score is a
number nobody can act on.

## Three disciplines that are easy to skip

**Evidence missing is failure.** A Case that declares required evidence and does
not get it **fails**; it must never pass because "no problem was observed".
This has already caught a real defect: an OTLP auth error pushed a cleanup
evidence field out of the report, and fail-closed turned it red instead of
silently green.

**Never mark an unrun command as passing.** Say what actually ran, with its exit
code. If a check could not run, it is `unverified`, not `pass`.

**Before reporting "the gate does not catch this", prove the fault injection is
effective.** An injection that lands on a code path the Case never reaches
produces a false "not caught" — worse than not testing, because it looks like
evidence. Confirm that the protection actually broke before blaming coverage.

## Closing the loop

A single run is one-way: run → report → done. The loop closes with three parts,
and dropping any one breaks it:

1. **Claims map to Cases.** `eval/coverage.json` maps each reviewed claim to the
   Cases that verify it, or waives it with a written reason. Check it with
   `cmd/eval -check-coverage`; CI runs it. A claim nothing verifies cannot hide
   behind a green run.
2. **Failures become action items.** `cmd/eval -triage <path>` turns each failed
   assertion into an actionable item: which Case, which assertion, which failure
   class, where to look first. The taxonomy → suspect mapping is fixed and
   small; an unknown class is flagged for human judgement rather than dropped.
3. **A fix needs a Case that would have caught it.** Reproduce the fault with
   `./eval/mutation-gate.sh` and require the new Case to turn red. If it does not
   turn red, the Case does not cover the defect, so the fix is not finished.

`eval/mutation-gate.sh` proves the gate is not decorative: it injects real
faults, runs L0/L1, and fails if any fault passes undetected. It rewrites source
files, so it must run **alone** — a concurrent build would compile injected code
(observed once; the script now takes a lock).

## Registry and version discipline

- **A new or changed Case set changes the dataset version.** Baselines bind to
  `dataset@version`; comparing across versions is refused as a setup error, and
  that refusal is correct — two different measurements are not a regression.
- **Regenerating an unchanged baseline keeps its approval time**, so a baseline
  diff always means the verdicts or versions actually changed.
- **When a protection cannot be reached end-to-end, say so and keep it at the
  layer that can reach it.** Record the code evidence (the guard clause and the
  path that makes it unreachable) in `PROGRESS.md` and put a waiver with that
  reason in `eval/coverage.json`. Never write a Case that pretends to cover it.

## What not to build

- **A judge in the release gate.** It drifts; one false alarm and people stop
  trusting the gate.
- **An abstraction with one implementation.** A registry, interface or plugin
  point added before a second real user exists is a liability — this was tried
  here and reverted.
- **A format for something that does not exist.** Defining a judge-calibration
  schema before any judge exists is speculating, not designing.
- **A general DSL that re-implements the test runner.** For code-level behavior,
  write a Go test. Reserve Cases for behavior that only the running system shows.
- **Coverage percentages as a goal.** They reward tests that execute lines, not
  tests that would fail.

## Reporting vocabulary

| Status | Meaning |
| --- | --- |
| `pass` | the check ran and passed, with its command and exit code |
| `partial` | some evidence exists, a required dimension does not |
| `fail` | the check ran and failed |
| `unverified` | the check did not run |
| `deferred` | deliberately outside this increment, with a reason |
| *structurally unreachable* | the protection cannot be reached from this layer — name the guard and the path |

State clearly which claims are verified by L0, which by L1, and which only look
verified. "The gate is green" means nothing unless you also say what it did not
look at.

## Entry points

```bash
gofmt -l cmd internal eval && go vet ./... && go run ./cmd/archcheck   # L0
go test ./... && go test -race ./...                                  # L0

go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v2.json \
  -check-coverage ./eval/coverage.json -config ./config.example.toml  # claims ↔ Cases

go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v2.json \
  -report ./tmp/eval-report.json -triage ./tmp/eval-triage.json \
  -baseline ./eval/baselines/synthetic-operations-v2.json \
  -config ./config.example.toml                                       # L1 + baseline

./eval/mutation-gate.sh                                               # proves the gate can fail
```

`cmd/eval` exit codes: `0` all selected Cases passed, `1` a deterministic
assertion failed, `2` a setup error (incomparable baseline, unknown fixture
name, invalid coverage) — a setup error writes no report.

Langfuse is an optional diagnostic sink, never the verdict owner: it receives
bounded, redacted scores, and its failure never changes the local result.
