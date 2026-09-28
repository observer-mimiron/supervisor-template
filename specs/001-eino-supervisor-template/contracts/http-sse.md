# HTTP and SSE Contract

## Endpoints

除 `GET /healthz` 外，所有接口都要求：

```text
Authorization: Bearer <token>
```

服务端将凭证映射为可信主体；`tenant_id`、`subject_id` 不从请求 JSON 读取。run 创建时绑定主体，后续重放、审批、恢复和取消只能由该主体执行。

请求可携带标准 W3C `traceparent` 和可选 `tracestate`。服务端提取合法 parent，创建 HTTP span，
并在响应 `X-Trace-ID`、`X-Request-ID` 中返回关联标识；非法传播头按缺失处理，不把原始 header
写入日志或事件。若请求生成 run，所有 SSE、Run、Worker、Tool、Checkpoint 和 Event 操作必须
继承同一 OTel context。

### `POST /api/chat`

请求：

```json
{
  "conversation_id": "demo",
  "message": "分析近 30 天未下单、历史消费满 1000 元、近 7 天未触达且合成同意状态允许的客户",
  "run_id": "optional-existing-run-id"
}
```

返回 `Content-Type: text/event-stream`。新请求生成 `run_id`；恢复已有 run 时必须校验主体、状态和会话归属。

计划中的运营案例固定使用以下合成数据场景：

- 单 Worker：筛选符合条件的合成客户，生成一个 `PlanStep`。
- 多 Worker：先筛选该客群，再基于有界结果生成汇总和跟进建议，最多两个有序 `PlanStep`。
- 模拟触达：另一个独立 run；必须先审批，当前只写入 fake executor。

这些示例消息是稳定的 HTTP fixture。未知、未注册或超过两步上限的候选计划必须拒绝，不能 fallback 到另一个 Worker。当前 fake Tool 仅回显输入，直到后续批次实现 fixture 查询前，不得把它的响应当作客群分析通过。


### `POST /api/runs/{run_id}/approval`

请求：

```json
{"decision":"approve"}
```

`decision` 只能是 `approve` 或 `reject`。审批人由认证主体的 `subject_id` 写入，客户端不能指定 `reviewer`。重复审批返回当前审批结果，不重复推进副作用。

### `POST /api/runs/{run_id}/resume`

无业务输入。服务端从 checkpoint 找到下一个可执行步骤；终态 run 重放原终态，不重新调用 Tool。

### `POST /api/runs/{run_id}/cancel`

无业务输入。服务端发出协作式取消信号；只有 Runner/Tool 观察到 `context` 取消后，服务端
才将未终态执行收口为 `canceled`，保存 checkpoint 和唯一取消事件。外部调用结果未知时，
返回 `RUN_OUTCOME_UNKNOWN`，不得自动重试或宣称已经停止。重复 cancel 只重放已有状态。

## SSE envelope

每条 SSE 的 `data` 是 JSON：

```json
{
  "event_id": "evt-1",
  "run_id": "run-1",
  "trace_id": "trace-1",
  "sequence": 1,
  "type": "progress",
  "data": {}
}
```

要求：

- `sequence` 从 1 开始并严格递增。
- 每个 run 最多发送一个 `completed`、`failed` 或 `canceled`。
- `approval_required` 后，未批准不得发出副作用 Tool 的成功事件。
- 多步骤运行至少能投影 `started -> decision -> plan`，随后每一步依序投影 `progress(running) -> tool_call -> progress(succeeded) -> text`，最后只有一个终态；`plan` 数据必须表达每个步骤的顺序、Worker 和 Tool。
- 模拟触达 run 必须先产生 `approval_required`；拒绝以一个 `failed` 终态结束且不得调用副作用 Tool，批准后只能由 `/resume` 推进。
- `reconciliation_required` 是非终态等待核对事件，不得被映射为成功或可自动重试的普通失败。
- 每个步骤都必须经过同一 Registry、Policy Gate 和 Runner 合同；SSE 不泄漏 Prompt、凭证或内部路径。
- 客户端断开不取消已保存的 checkpoint；服务端取消时必须写入 `canceled`。
- 事件追加按稳定 `event_id` 幂等；状态已保存但事件缺失时，恢复流程可以补发投影。
- SSE 的 `trace_id` 只用于关联，不作为业务状态；客户端断开或观测 exporter 失败不改变 checkpoint、
  审批、幂等或终态。

## 错误合同

错误使用稳定分类，例如 `UNAUTHENTICATED`（HTTP 401）、`ACCESS_DENIED`（HTTP 403）、`INVALID_REQUEST`、`UNKNOWN_CAPABILITY`、`POLICY_DENIED`、`APPROVAL_REQUIRED`、`TOOL_TIMEOUT`、`INVALID_OUTPUT`、`CANCELED`、`RUN_OUTCOME_UNKNOWN`、`RUN_NOT_RESUMABLE` 和 `INTERNAL_ERROR`。公开错误只能包含分类、用户可理解的消息、`run_id` 和已创建的 `trace_id`，不得暴露凭证、原始 Prompt、stack trace 或内部文件路径。内部日志/Trace 另保留 `error_class`、`phase`、`retry_decision` 和安全 fingerprint。

`RUN_OUTCOME_UNKNOWN` 表示外部调用已经开始但结果提交未确认。它对应 `waiting_reconciliation` 等待状态，不能被映射为成功或普通可重试失败，也不能自动再次调用副作用 Tool；客户端只能收到等待/核对信息，不收到第二个终态事件。
