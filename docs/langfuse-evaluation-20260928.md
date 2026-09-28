# Langfuse Runtime Evaluation (2026-09-28)

This is a post-M9-M11 runtime evaluation of the local fake-model HTTP chain. It
is evidence that traces were exported and that the main state transitions were
observable; it is not an online LLM quality or Judge evaluation.

## Preconditions

- Langfuse `http://localhost:3001/api/public/health` returned `200` with `status: OK`.
- OTLP HTTP authentication came from an environment file outside this repository and was not copied into it.
- The service ran on host port `18102`; `MODEL_PROVIDER=fake` was explicit.
- The exporter endpoint was `http://localhost:3001/api/public/otel`. The runtime appends `/v1/traces` and `/v1/metrics`.
- Requests used the local `demo-token`; no external model or business write was used.

## Cases and structural scores

| Case | Request / transition | Langfuse trace(s) | Result | Score |
|---|---|---|---|---:|
| Read-only | `请分析这个用户` -> `user_query` | `2e4dce64738691422d500b4727bc7a58` | one `http.server`, 8 `run.event` observations, terminal `completed` | 1.0 |
| Serial | `请先分析再总结` -> `user_query` -> `user_summary_query` | `d6471c56349617c7a5ed58540405538e` | 13 observations, both tool calls and one terminal `completed` | 1.0 |
| Approval | `请模拟触达用户` -> approval -> POST resume | `f1dbf46a359994623c7dc3f6b4536593`, `8e5b7f77da867f7a0f78e042c9f4c9bd` | approval trace has no Tool execution; resume trace has one Tool call and terminal `completed` | 1.0 |
| Idempotent replay | duplicate POST resume | `2b92a919ae956801f7a7d966851c5c8f` | replay is observable as an HTTP span; application events remain the single saved terminal sequence | 1.0 |

Scoring is deterministic: `1.0` means the expected event/state invariant was
observed, `0.0` means it was not. No Langfuse score API or LLM Judge was used.

## Failure classification

The first attempt used the base URL without the OTLP signal suffix. Langfuse
returned `404` for `/api/public/otel` while `/api/public/otel/v1/traces`
returned `200`; no business request was marked successful on that evidence.
`normalizeOTLPEndpoint` now appends the signal path, covered by a unit test, and
the cases above were rerun after the fix.
