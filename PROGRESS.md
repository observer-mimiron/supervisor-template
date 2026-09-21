# Progress

## 当前阶段

- M0-M3 已实施完成；默认仍是 fake/内存路径，真实依赖通过配置选择。
- 运行链路为 `HTTP/SSE -> Application -> Supervisor -> Policy Gate -> Manager -> Tool -> Final Guard -> Event`。
- M4 尚未开始：持久化 checkpoint、MCP、认证、多租户和具体运营业务仍是后续范围。

## 已验证事实

- 已建立 Go module、`cmd/server/` 和 `internal/{domain,application,interfaces,infrastructure,composition}/`。
- 配置按 `TOML -> 环境变量 -> Validate` 加载；Supervisor 的 `allowed_workers`、Worker 的 Tool 白名单和实现标识均启动时校验。
- 领域合同覆盖 SupervisorDecision、ApprovedRoute、ExecutionPlan、ApprovalRequest、Checkpoint 和 RunEvent。
- Policy Gate 拒绝未知 Worker/Tool、越权风险和未批准副作用。
- fake `user_query` 只读执行；fake `simulated_outreach` 使用幂等键，批准前不会执行。
- 运行服务拥有计划和终态；重复 resume、重复审批和重复 cancel 不新增终态事件。
- Final Guard 拒绝空结果、凭证标记、Prompt 标记和内部路径，拒绝结果不会投影为成功文本。
- 显式 cancel 写入 `canceled`；context cancel 和 deadline 分别映射为取消与 `TOOL_TIMEOUT`。
- 已存在 run_id 必须属于同一 conversation，否则不重放原事件。
- `go.work` 使用当前模板 module，模板目录内可直接运行普通 Go 命令。
- 已新增 Prompt 资产和 Supervisor Decision JSON Schema，且不包含密钥或真实用户数据。
- `model.provider = "deepseek"` 使用官方 Eino DeepSeek 适配器；API key 只从 `model.api_key_env` 指定的环境变量读取。
- 真实模型输出经过严格 JSON 解析，决策 ID 由运行上下文生成，随后仍进入注册表和 Policy Gate。
- `tools.user_query.implementation = "http.read_only"` 使用固定 endpoint 的标准库 HTTP GET；默认 `fake.user_query` 不变。
- `observability.enabled = true` 装配 OTLP HTTP trace/metric exporter 和 Eino ChatModel/Tool Callback；事件与回调只记录结构信号，不记录消息正文。
- 本地合同测试、官方适配器本地 HTTP smoke、真实 Tool 装配级 smoke 和本地 OTLP collector smoke 已覆盖 M3。

## 当前验证命令

```bash
go list ./...
go test ./...
go test -race ./...
go build ./cmd/server/
go vet ./...
git diff --check
```

以上命令应在模板目录执行；服务 smoke 使用：

```bash
go run ./cmd/server/ -f ./config.example.toml
```

然后按 `specs/001-eino-supervisor-template/quickstart.md` 验证只读、审批、resume 和 cancel。

## 未完成范围

- 未使用真实外部模型凭证或外部 Tool 地址执行线上 smoke；本地 HTTP stub 已验证官方适配器和应用链路。
- M4：持久化 checkpoint、MCP、认证、多租户和具体运营业务。
- 当前 fake 服务是单进程内存实现，不承诺跨进程恢复或高并发吞吐。

## 关键约束

- `.specify/memory/constitution.md` 未修改，仍是项目唯一宪法。
- Domain 不依赖 Gin、Eino、数据库、MCP、具体模型或 SSE。
- 公开事件和错误不包含凭证、原始 Prompt、stack trace 或内部文件路径。
- 任何新增副作用仍需 `Policy Gate -> Approval -> Idempotency -> Tool -> Audit Event`。
