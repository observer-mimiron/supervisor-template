# Evaluation Contract

## Dataset and Case JSON

`eval/datasets/*.json` is the human-maintained source of acceptance standards. Follow the dataset envelope used in `/home/huang/workspace/suanming-agent/eval/datasets/runtime-smoke-v2.json`:

```json
{
  "name": "synthetic-operations",
  "version": "1",
  "description": "Local API/SSE evaluation for the synthetic operations example",
  "cases": [
    {
      "id": "positive-audience-query",
      "version": "1.0.0",
      "category": "positive",
      "risk_level": "low",
      "impact_tags": ["audience-query"],
      "business_goal": "固定快照返回正确匿名客群",
      "preconditions": {"fixture": "synthetic_audience_v1", "subject": "demo-user"},
      "request_steps": [
        {"action": "chat", "message": "分析沉睡客户", "conversation_id": "eval-positive-1"}
      ],
      "expected_results": {
        "terminal": "completed",
        "event_types": ["started", "decision", "plan", "progress", "tool_call", "text", "completed"],
        "result": {"count": 4, "customer_ids": ["cust-001", "cust-002", "cust-006", "cust-008"], "spend_365d_total": 6200}
      },
      "forbidden_side_effects": {"tool_ids": ["simulated_outreach"], "max_writes": 0},
      "evidence_requirements": ["events", "tool_calls", "run_state", "trace_correlation"],
      "evaluator_rules": ["business_correctness@1", "architecture_boundary@1", "side_effect_safety@1", "stability@1"],
      "timeout": "5s",
      "retry_budget": 1,
      "idempotency_key": "eval-positive-1",
      "cleanup_policy": "isolated_run"
    }
  ]
}
```

Every Case has the required fields shown above. The loader rejects duplicate JSON object keys, unknown fields, invalid enum values, missing requirements, duplicate `id + version`, nonpositive timeouts, negative retry budgets, unknown fixtures/evaluators, dynamic expressions and secret-like values. Expected results and evaluator rules are read-only during execution; generated steps may not alter them.

`request_steps.action` is limited to `chat`, `approval`, `resume`, `cancel`, and `repeat`. Approval values are limited to `approve`/`reject`; request fields must match the existing HTTP contract. Cases cannot contain credentials, real personal data, arbitrary URLs, executable code or model-generated assertions.

The initial dataset must cover four categories:

- `positive`: read-only query, two-step summary, approval-gated outreach, correct final result.
- `negative`: unapproved side-effect attempt, cross-subject run access, unknown Worker/Tool, invalid input/empty result, privilege escalation.
- `boundary`: duplicate resume/approval/outreach, interruption recovery, deadline, Tool timeout, checkpoint/result-commit failure.
- `diversity`: changed synthetic input, subject, initial state, risk and execution path.

Case 的 impact_tags 是人工维护的业务/架构范围标签。PR 的风险等级和受影响标签也由人声明；生成代码或 Judge 结论不得降低风险或收窄测试集。风险不明时按 high 处理。

## Runner and Evidence

The local Runner drives the existing routes: `POST /api/chat`, `POST /api/runs/{run_id}/approval`, `/resume`, `/cancel`. It parses the existing SSE envelope and records `run_id`, `case_id`, `case_version`, `code_version`, `trace_id`, non-empty unique event IDs, event order, Tool calls, approval/recovery/cancel/terminal status, timeout/retry, fake write count and cleanup result. A single Case deadline covers setup plus all follow-up requests. Only safe pre-call failures may consume retry budget; unknown outcomes are not retried. Evidence integrity rejects missing or mismatched Case/Run association and rejects events without a non-empty ID or matching RunID.

Runner exit codes: `0` all deterministic hard checks pass; `1` business execution or hard assertion failed; `2` invalid dataset/configuration or Runner setup failure. A Runner failure cannot be reported as an evaluator pass.

## Deterministic Evaluators

1. `business_correctness`: expected response/result fields, event order, one terminal event and ordered multi-step execution.
2. `architecture_boundary`: only registered/allowed Worker and Tool, authorization and Policy Gate evidence, no forbidden cross-domain operation, and static Go package dependency direction checks (`interfaces -> application -> domain`, infrastructure contracts inward, composition wiring only).
3. `side_effect_safety`: no write before approval, no duplicate write after repeated execution, Tool-call budget and fixed idempotency behavior.
4. `stability`: repeated Case gives the same deterministic verdict, timeout/retry stay within budget, and failures identify Case/run/step.

Each failure includes `case_id`, `case_version`, `code_version`, `run_id`, `evaluator_name`, `evaluator_version`, `failed_assertion`, `evidence_reference`, and nullable `human_disposition`. Keep evaluator-level results and reasons; do not make one aggregate score the only result.

## Optional Judge and Feedback

The Judge is disabled by default or backed by a local fake; it never changes hard-gate exit status. Its JSON result includes `rubric_version`, `score`, `risk_level`, `finding`, `evidence_reference`, `confidence`, `recommendation`, `requires_human_review`, `model`, `prompt_version`, `input_digest`, `output_digest`, and `evaluated_at`.

Failures are not automatically added to the dataset. A human disposition classifies business expectation, implementation, architecture, evaluator rule, Case data, environment/dependency, or Judge error and explicitly chooses whether to add a Case, deterministic rule, or Rubric revision.

## PR Risk and CI Gate

- Every PR declares a risk level and affected tags. Low/medium changes run baseline checks plus Cases matching the declared tags; unclear scope is high risk.
- High-risk changes include changes to Policy Gate, authentication/resource authorization, approval, idempotency, execution state ownership, persistence, external side effects, or dependency direction. They run the full Case dataset and require a human reviewer disposition with reviewer, decision, rationale, and timestamp.
- Go tests, static package-dependency checks, and deterministic Case assertions are hard gates. Any failure returns nonzero and blocks merge. LLM Judge output is advisory only and never changes a hard-gate result.
- A failed/missing required human disposition blocks high-risk merge independently of evaluator results. Repository branch protection/required-review settings must be enabled; a workflow file alone cannot enforce platform merge policy.
- Failure disposition is not the same as high-risk PR approval. Only a human-confirmed failure may create a versioned Case, deterministic rule, or Rubric update; the next associated regression run must execute that new version and report it.

The PR workflow runs the architecture checker, `go test ./...`, evaluation package tests, and the local Case Runner without network credentials or external services. It selects matching Cases by explicit impact tags for ordinary changes and all Cases for high-risk changes. Required status checks/reviews must be enabled in repository settings; before that, workflow results are advisory rather than merge-blocking.

## Redaction

Reports, evidence and Judge input exclude raw sensitive user data, Bearer/API keys, complete prompts, full Tool input/output, raw SQL/external response, internal paths and unsanitized stack traces. Current synthetic Cases use fake state and write counters; SQL must not be invented where no database operation exists.
