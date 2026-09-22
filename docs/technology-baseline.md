# Technology Baseline

## Purpose

本文件是模板技术选型和基础设施边界的唯一来源。它记录当前实现采用什么、为什么采用、允许替换什么，以及替换时必须验证什么。

本文件不替代项目宪法。领域语义、安全边界、状态所有权、权限上限和运行合同仍以 [`.specify/memory/constitution.md`](../.specify/memory/constitution.md) 为准。

## Baseline

| 领域 | 基线 | 责任边界 |
|---|---|---|
| 服务端语言 | Go 1.25 | 进程生命周期、`context`、超时、取消和基础并发 |
| Web | Gin 1.12.x | HTTP、SSE、middleware、健康检查和优雅关闭；不推进业务状态 |
| Agent 编排 | Eino 0.9.12 | Supervisor、Worker、Runner、Tool calling、Callback、interrupt/resume |
| 真实模型适配 | Eino DeepSeek 0.1.6 | 可选的真实 ToolCallingChatModel；密钥只从环境变量读取 |
| 配置 | BurntSushi TOML 1.4 | typed config、文件加载、环境变量覆盖和启动校验 |
| 身份/资源授权 | 标准库 SHA-256 Bearer 适配器 + run owner 授权 | `/api/*` 先认证主体；run 的重放、审批、恢复和取消按主体隔离；不引入 JWT/OIDC/RBAC 依赖 |
| 日志 | zap | 结构化日志和脱敏字段 |
| 观测 | OpenTelemetry 1.43.0 + OTLP HTTP | Eino Callback、run event Trace、可选 Metric 和跨组件关联 |
| v1 存储 | 内存 Repository、内存 checkpoint；可选文件 Repository/checkpoint/event | 默认只验证合同；文件实现使用版本化 JSON、哈希路径和临时文件 rename，支持跨进程恢复但不承担数据库级高可用 |
| v1 外部能力 | fake 默认；可选 DeepSeek、HTTP 只读 Tool 和受控 MCP Tool | 不连接真实 CRM、营销渠道或外部写入系统；MCP 只允许启动时注册的 server/tool，具备超时、输入/响应大小限制和错误分类 |

版本号以实现时 `go.mod` 的锁定版本为准；本表使用主版本范围，避免方案文件成为未验证的依赖锁文件。

## M3 实现状态

- `model.provider = "fake"` 保持默认；`deepseek` 使用官方 Eino 扩展，真实模型输出先做严格 JSON 解析，再进入既有 Policy Gate。
- `tools.user_query.implementation = "http.read_only"` 可接入一个固定 endpoint 的 HTTP GET 只读能力；Tool ID、Worker allow-list、超时和 Final Guard 不变。
- `observability.enabled = true` 时装配 OpenTelemetry OTLP HTTP trace/metric exporter，并安装 Eino ChatModel/Tool Callback；默认只记录结构信号和 token 数，不记录消息正文。
- `LLM_API_KEY`、`OTEL_EXPORTER_OTLP_HEADERS` 等凭证只从环境变量读取，不进入 TOML、事件或 checkpoint。
- M4 本轮已实施 T016 持久化 checkpoint、T017 受控 MCP Tool 和 T018 的最小认证/资源隔离边界；静态 Bearer 只适合本地/受控环境，完整 OIDC/JWT、RBAC 和 T019 具体运营业务延期。
- 认证凭证只在环境变量中保存 SHA-256；主体的租户和用户标识随 run 请求快照保存，用于资源所有权校验，不保存原始 token。

## Dependency Placement

```text
interfaces/http -> application -> domain
composition     -> application + domain + infrastructure
infrastructure  -> domain/application contracts
```

- `Gin` 只能出现在 `internal/interfaces/http` 和 HTTP 相关基础设施。
- `Eino`、模型 SDK、MCP、数据库、缓存、事件总线和 OpenTelemetry 只能出现在 `internal/infrastructure`。
- `domain` 不依赖 Gin、Eino、数据库、MCP、具体模型或 SSE。
- `composition` 只负责依赖装配，不承载业务判断。
- 新增 Worker 或 Tool 应通过注册表和装配层接入，不修改主执行循环。

## Configuration Contract

配置加载顺序固定为：

```text
命令行 -f 指定文件 -> TOML 文件 -> 环境变量覆盖 -> 启动校验
```

必须配置化：

- HTTP 地址、超时、请求体上限、SSE 心跳和 middleware 开关
- 模型供应商、模型名、Base URL、温度、Token/成本预算和模型超时
- 真实模型的 API key 环境变量名
- Worker/Tool 启用状态、实现 ID、Prompt 文件和允许工具
- HTTP 只读 Tool 的固定 endpoint、超时和重试
- 步骤、重试、调用次数、审批超时、幂等窗口和 checkpoint 保留策略
- 日志级别、Trace 采样率和外部服务地址
- OTel 是否启用、service name、OTLP endpoint 和是否使用明文传输
- 认证实现 ID、Bearer 哈希环境变量名、租户 ID 和主体 ID

必须由代码固定：

- Schema 合同、状态转换、状态所有权和终态规则
- 权限上限、Policy Gate、审批不可绕过规则和幂等语义
- 已注册 Worker/Tool 的实现集合

配置不能创造不存在的实现、扩大权限、降低审批等级、绕过 Schema、改变状态所有权或关闭安全门禁。配置变更默认重启生效，v1 不实现热加载。

## Implementation Boundary

基础设施只使用 `go.mod` 中声明并经过验证的组件。新增依赖必须说明用途、边界和验证方式；实现不能绕过既有的领域合同、Policy Gate 或状态所有权。

## Licensing

- 本项目原创代码和文档采用根目录 [MIT License](../LICENSE)。
- `go.mod` 中的第三方依赖仍受各自项目许可证约束，不因本项目采用 MIT 而改变。

## Change Procedure

以下变更必须先更新本文件、Spec/Plan 或 ADR，并补充验证：

1. 替换 Gin、Eino、配置、日志或观测组件。
2. 改变依赖目录、运行链路或基础设施 owner。
3. 引入数据库、缓存、消息队列、真实外部写入或热加载。
4. 增加新的部署环境或新的模型供应商。

如果变更同时改变领域合同、状态所有权、权限边界或宪法目录约束，必须走宪法修订流程；技术基线文件不能自行放宽宪法规则。

## Verification

每次技术基线变更至少验证：

- `go test ./...`
- 受影响模块的合同测试
- HTTP/SSE smoke
- 依赖方向和禁止依赖审计
- 配置加载、环境变量覆盖和非法配置启动失败
- 未认证请求、跨主体 run 操作和审批主体记录合同
