# MySQL/GORM Tool Contract

## Scope

This contract adds two registered Tools to the existing `/api/chat` flow. It does not add an HTTP endpoint and does not change the Domain/Application contracts.

## Registered capabilities

| Tool ID | Risk | Approval | Implementation |
|---|---|---|---|
| `mysql_order_query` | `read_only` | no | `gorm.mysql_order_query` |
| `mysql_order_insert` | `side_effect` | yes | `gorm.mysql_order_insert` |

Both Tools belong to the registered MySQL example Worker and must be present in that Worker allow-list. Configuration can select these IDs only; it cannot lower the insert approval requirement.

## Input contract

The Tool receives the existing `message` string field and parses strict JSON.

Query:

```json
{"user_id":1}
```

or:

```json
{"order_id":1}
```

Exactly one selector is required. Insert:

```json
{"user_id":1,"product_id":1,"quantity":2,"total_amount":"39.98"}
```

Unknown fields, missing fields, non-positive IDs/quantity, invalid decimal values, control characters, trailing JSON, and payloads over the registered byte limit are rejected before GORM is called.

## Output contract

Query returns a bounded JSON projection of matching orders. Insert returns the created order ID and normalized fields. Neither operation returns a password, DSN, SQL statement with values, stack trace, filesystem path, or arbitrary database columns.

## Side-effect and idempotency contract

`mysql_order_insert` must not acquire a database transaction before the application records `approval=approved`. The application derives the idempotency key from `run_id + step_id`; the client and model cannot provide it. A repeated approved resume with the same key returns the stored result and creates no additional order.

## Error contract

- Invalid input: `INVALID_OUTPUT`/validation classification, no SQL call.
- Missing user/product or foreign-key failure: business failure, no successful projection.
- Connection refused or unavailable database before call: pre-call/unavailable classification and bounded retry according to the existing error policy.
- Context deadline/cancel: timeout/canceled classification.
- Commit result unknown: `RUN_OUTCOME_UNKNOWN`; no automatic reinsert.

## OpenTelemetry contract

The GORM database is opened with the MySQL driver and `tracing.NewPlugin(tracing.WithoutQueryVariables())`. Every operation uses `db.WithContext(ctx)`. Query and insert spans must include database system/name and operation attributes while excluding user payloads and query variables. The span must be a child of the current Tool/run span and use the existing configured tracer provider.

## Compose contract

`docker-compose.yml` starts only MySQL by default. The `observability` profile adds an OTLP/HTTP Collector. The Go application remains a host process. The MySQL container exposes `3306`, has a health check, and mounts the idempotent initialization SQL from `internal/infrastructure/persistence/testdata`.
