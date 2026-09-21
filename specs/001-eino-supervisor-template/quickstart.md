# Quickstart

当前状态：M0-M2 fake 实现已完成并通过本地合同测试；M3/M4 仍是后续阶段。实施任务见 [`tasks.md`](./tasks.md)，目录和依赖方向见 [`../../docs/architecture.md`](../../docs/architecture.md)，状态快照见 [`../../PROGRESS.md`](../../PROGRESS.md)。

## 前置条件

- Go 1.25+
- `curl` 和 `jq`
- 不需要真实 LLM、CRM 或营销渠道；默认使用 fake model 和 fake Tool

配置示例使用根目录的 [`config.example.toml`](../../config.example.toml)。运行时按 `-f` 指定配置文件，再用环境变量覆盖；模板不在代码中写死部署地址、模型、Prompt、能力开关或预算。

## 验证顺序

```bash
go list ./...
go test ./...
go test -race ./...
```

M1 的第一条硬验收链路是：`Gin -> Application -> Eino Supervisor -> fake Worker -> fake Tool -> RunEvent -> SSE`。

启动本地 fake 服务：

```bash
FAKE_MODEL=true go run ./cmd/server/ -f ./config.example.toml
```

环境变量覆盖示例：

```bash
LISTEN_ADDR=:18080 LOG_LEVEL=debug go run ./cmd/server/ -f ./config.example.toml
```

只读 smoke：

```bash
curl -N -X POST http://127.0.0.1:8080/api/chat \
  -H 'Content-Type: application/json' \
  -d '{"conversation_id":"demo","message":"分析示例用户分群"}'
```

预期：收到 `started`、`decision`、`plan`、`progress`、`text` 和唯一 `completed`；响应中不出现内部路径或凭证。

审批 smoke：

```bash
curl -N -X POST http://127.0.0.1:8080/api/chat \
  -H 'Content-Type: application/json' \
  -d '{"conversation_id":"demo","message":"模拟触达示例用户"}'

RUN_ID=上一步响应中的_run_id
curl -X POST "http://127.0.0.1:8080/api/runs/$RUN_ID/approval" \
  -H 'Content-Type: application/json' \
  -d '{"decision":"approve","reviewer":"operator-1"}'

curl -N -X POST "http://127.0.0.1:8080/api/runs/$RUN_ID/resume"
```

预期：批准前模拟写入次数为 0；批准并重复 resume 后写入次数仍为 1，最终结果相同。

取消 smoke：

```bash
curl -N -X POST "http://127.0.0.1:8080/api/runs/$RUN_ID/cancel"
```

预期：返回已有事件和唯一 `canceled` 终态；重复 cancel 不新增终态事件。

## 模块验收

- M0：领域合同、注册表、状态转换和内存 checkpoint 测试。
- M1：只读 `/api/chat` smoke、未知能力拒绝和 fake model 性能测试。
- M2：审批批准/拒绝、重复恢复和幂等计数测试。
- M3：中断后恢复、已完成步骤不重跑和终态重放测试。
- M4：SSE 顺序、唯一终态、错误脱敏、取消和全量测试。
