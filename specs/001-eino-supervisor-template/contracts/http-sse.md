# HTTP and SSE Contract

## Endpoints

### `POST /api/chat`

请求：

```json
{
  "conversation_id": "demo",
  "message": "分析示例用户分群",
  "run_id": "optional-existing-run-id"
}
```

返回 `Content-Type: text/event-stream`。新请求生成 `run_id`；恢复已有 run 时必须校验其状态和会话归属。

### `POST /api/runs/{run_id}/approval`

请求：

```json
{"decision":"approve","reviewer":"operator-1"}
```

`decision` 只能是 `approve` 或 `reject`。重复审批返回当前审批结果，不重复推进副作用。

### `POST /api/runs/{run_id}/resume`

无业务输入。服务端从 checkpoint 找到下一个可执行步骤；终态 run 重放原终态，不重新调用 Tool。

### `POST /api/runs/{run_id}/cancel`

无业务输入。服务端将未终态执行标记为 `canceled`，保存 checkpoint 和唯一取消事件；重复 cancel 只重放已有终态。

## SSE envelope

每条 SSE 的 `data` 是 JSON：

```json
{
  "event_id": "evt-1",
  "run_id": "run-1",
  "sequence": 1,
  "type": "progress",
  "data": {}
}
```

要求：

- `sequence` 从 1 开始并严格递增。
- 每个 run 最多发送一个 `completed`、`failed` 或 `canceled`。
- `approval_required` 后，未批准不得发出副作用 Tool 的成功事件。
- 客户端断开不取消已保存的 checkpoint；服务端取消时必须写入 `canceled`。

## 错误合同

错误使用稳定分类，例如 `INVALID_REQUEST`、`UNKNOWN_CAPABILITY`、`POLICY_DENIED`、`APPROVAL_REQUIRED`、`TOOL_TIMEOUT`、`INVALID_OUTPUT`、`CANCELED`、`RUN_NOT_RESUMABLE` 和 `INTERNAL_ERROR`。公开错误只能包含分类、用户可理解的消息和 `run_id`，不得暴露凭证、原始 Prompt、stack trace 或内部文件路径。
