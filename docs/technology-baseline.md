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
| 日志 | zap | 结构化日志和脱敏字段 |
| 观测 | OpenTelemetry 1.43.0 + OTLP HTTP | Eino Callback、run event Trace、可选 Metric 和跨组件关联 |
| v1 存储 | 内存 Repository、内存 checkpoint | 只验证合同和恢复语义，不承担生产持久化 |
| v1 外部能力 | fake 默认；可选 DeepSeek 和 HTTP 只读 Tool | 不连接真实 CRM、营销渠道或外部写入系统；HTTP Tool 只发送固定地址 GET |

版本号以实现时 `go.mod` 的锁定版本为准；本表使用主版本范围，避免方案文件成为未验证的依赖锁文件。

## M3 实现状态

- `model.provider = "fake"` 保持默认；`deepseek` 使用官方 Eino 扩展，真实模型输出先做严格 JSON 解析，再进入既有 Policy Gate。
- `tools.user_query.implementation = "http.read_only"` 可接入一个固定 endpoint 的 HTTP GET 只读能力；Tool ID、Worker allow-list、超时和 Final Guard 不变。
- `observability.enabled = true` 时装配 OpenTelemetry OTLP HTTP trace/metric exporter，并安装 Eino ChatModel/Tool Callback；默认只记录结构信号和 token 数，不记录消息正文。
- `LLM_API_KEY`、`OTEL_EXPORTER_OTLP_HEADERS` 等凭证只从环境变量读取，不进入 TOML、事件或 checkpoint。
- M4 的持久化 checkpoint、MCP、认证、多租户和具体运营业务仍未实施。

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

必须由代码固定：

- Schema 合同、状态转换、状态所有权和终态规则
- 权限上限、Policy Gate、审批不可绕过规则和幂等语义
- 已注册 Worker/Tool 的实现集合

配置不能创造不存在的实现、扩大权限、降低审批等级、绕过 Schema、改变状态所有权或关闭安全门禁。配置变更默认重启生效，v1 不实现热加载。

## Reference Boundary

`/home/huang/workspace/go-porter` 只作为 Gin 基础设施参考，允许借鉴配置入口、ServiceContext 装配、middleware 组合、健康检查、配置快照和优雅关闭方式。

模板不依赖 go-porter 包，也不复制其业务 handler、认证、MySQL/Redis 默认依赖或业务配置。来自外部项目的直接代码或明显改写必须登记到 `docs/upstream-map.md`，并保留许可证信息。

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
