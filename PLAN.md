# Eino Supervisor Template 实施方案

## 1. 目标

创建一个独立的 Go/Eino Agent 参考项目，采用经过调研的 DDD 分层原则，保留 `suanming-agent` 的 Supervisor/Manager 运行模式，为后续运营 Agent 提供可复制的最小骨架。Coze Studio 仅作为成熟开源实现的参考来源，不是本项目规范。

项目不承载命理业务，不复制完整 Coze 平台，不实现真实运营系统写入。

当前状态以 [PROGRESS.md](./PROGRESS.md) 为准：M0-M2 fake 实现已完成，M3/M4 仍是后续阶段。目录职责和依赖方向以 [docs/architecture.md](./docs/architecture.md) 为准，实施任务以 [specs/001-eino-supervisor-template/tasks.md](./specs/001-eino-supervisor-template/tasks.md) 为准。设计纲领以 [.specify/memory/constitution.md](./.specify/memory/constitution.md) 为准：领域边界和类型契约优先于框架与配置；LLM 只提出经过校验的候选决策；所有副作用都经过策略、审批、幂等和审计；默认选择最小可运行实现，需求出现后再增加分布式和平台能力。具体技术选型和升级规则以 [docs/technology-baseline.md](./docs/technology-baseline.md) 为准。

## 1.1 技术栈与配置优先

- Go 1.25：服务端语言，使用标准库完成进程生命周期、context、超时、取消和基础并发。
- Gin：HTTP API、SSE、middleware、健康检查和优雅关闭；路由只做协议适配，不推进业务状态。
- Eino：Supervisor、Worker、Tool calling、Runner、Callback、interrupt/resume；Eino 只位于 `infrastructure/eino`。
- Viper + TOML：配置文件加载和环境变量覆盖；配置结构使用 Go typed struct，启动时一次性校验。
- zap：结构化日志；OpenTelemetry：Trace/Metric；两者均由基础设施实现注入。
- v1：内存 Repository、内存 checkpoint、fake model、fake Tool；不预先引入数据库、Redis、消息队列或真实营销渠道。

配置加载顺序固定为：`命令行 -f 指定文件 -> TOML 文件 -> 环境变量覆盖 -> 启动校验`。默认配置在重启后生效，v1 不实现热加载。

这里的“配置优先”只针对部署和可变策略参数；业务语义、安全边界和状态合同仍遵循“宪法/合同优先于配置”。配置是已注册能力的选择器，不是动态造能力或改规则的入口。

必须配置化的内容包括 HTTP 地址和超时、SSE 心跳与请求体上限、中间件开关、模型供应商/模型名/Base URL/温度/预算/超时、Worker/Tool 启用状态与实现 ID、Prompt 文件、允许工具、步骤/重试/调用上限、审批超时、幂等窗口、checkpoint 保留策略、日志级别、Trace 采样率和外部服务地址。

必须由代码固定的内容包括安全不变量、Schema 合同、状态转换、权限上限、Policy Gate、审批不可绕过规则、终态规则和状态所有权。配置不能创造不存在的实现、扩大权限、降低审批等级、绕过 Schema 或改变状态所有权。

## 2. 参考基线

- Coze Studio：`/home/huang/workspace/suanming-agent/agent-architecture-references/coze-studio`
- Coze 基线：`fefb05ff`
- Coze 后端结构：`api/application/domain/infra/crossdomain`
- Coze 许可证：Apache 2.0
- 当前项目执行模式：`RouteAdvisor/Supervisor -> Policy Gate -> Manager -> bounded runner -> ToolRunner -> Event Projection`
- Eino 能力：ChatModel、ChatModelAgent、Runner、Tool calling、Callback、interrupt/resume
- go-porter：仅参考其配置加载、Gin middleware、健康检查、ServiceContext 装配和优雅关闭；模板不依赖其包，也不复制其业务 handler、认证、数据库或缓存默认实现。

## 3. 第一版范围

必须实现：

1. `POST /api/chat`，支持 SSE。
2. 一个 Supervisor，输出结构化路由决策。
3. 一个运营 Worker，例如用户分析或活动分析。
4. 一个只读 Tool。
5. 一个需要审批的模拟副作用 Tool。
6. Policy Gate、审批等待和 resume。
7. 内存会话、执行状态和 checkpoint 接口。
8. Eino Agent/Runner 适配。
9. 内部 RunEvent 到 SSE 的投影。
10. 本地零模型合同测试和一个真实入口 smoke 测试。

暂不实现：

- CRM、订单、发券、群发、真实营销渠道。
- 完整工作流编辑器和 DSL。
- 插件市场、知识库管理后台、多租户管理后台。
- 分布式消息队列和复杂数据库集群。
- 多 Supervisor 协商和开放式自主循环。

## 4. 运行链路

固定链路和各层禁止事项见 [docs/architecture.md](./docs/architecture.md)。运行链路为：`HTTP/SSE -> Application -> Supervisor -> Policy Gate -> Manager / ExecutionPlan -> bounded Worker -> allow-listed Tool -> Final Guard -> RunEvent -> SSE Projection`。

Supervisor 产出候选决策；Policy Gate 产出 `ApprovedRoute`；Manager 生成有界计划；Worker 返回结构化结果；最终答复由运行用例统一生成。

## 5. 领域边界

### agent

定义 Agent、路由决策、执行计划、Worker 能力和运行状态。Supervisor 是决策服务，Manager 是应用执行服务。

### conversation

定义会话、消息、用户输入和对话生命周期。它不负责选择 Worker，也不直接调用模型。

### operation

定义运营业务能力。第一版只放一个示例领域，例如 `user_analysis`；Worker 必须属于具体运营能力，不建立通用万能 Worker。

### approval

定义审批请求、审批状态、审批人和 resume 合同。审批结果不能由模型生成。

### tool

定义工具能力、风险等级、输入输出和副作用声明。真实外部系统实现放在 `infrastructure`。

## 6. 基础设施复用矩阵

### 可直接移植机制

- SSE 流式输出和事件投影模式。
- checkpoint、恢复和 interrupt/resume 接口模式。
- Event envelope、Run ID、时间戳和状态字段。
- 日志、Trace、Callback 和成本记录模式。
- ID 生成、错误分类和 Repository 接口模式。
- Eino Runner、Tool calling、Callback 的适配方式。
- go-porter 的 Gin Engine 初始化、middleware 组合、健康检查、配置快照和优雅关闭方式。

移植时必须清理 Coze 内部包名、平台错误码和厂商模型。

### 需要改写

- `infra/checkpoint`
- `infra/eventbus`
- `infra/cache`
- `infra/storage`
- `infra/rdb`
- `infra/embedding`
- LLM/model builder
- Eino workflow/service 适配
- 应用初始化和配置装配
- go-porter 的业务模块、认证、MySQL/Redis 默认依赖和领域 handler

### 只作参考

- 多租户权限。
- 插件市场。
- 完整 Workflow DSL 和节点运行时。
- 生成的 IDL、ORM、DAL 和 API model。
- Coze 专用云服务、图片服务和渠道连接器。

## 7. 核心合同

第一版至少定义以下结构化合同：

```text
SupervisorDecision
ApprovedRoute
ExecutionPlan
WorkerContract
ToolContract
RunEvent
Checkpoint
ApprovalRequest
```

合同要求：

- 模型输出必须解析为结构化数据。
- 未知 Worker 或 Tool 必须拒绝。
- 每个计划有最大步骤数和超时。
- 每个副作用 Tool 有风险等级和幂等键。
- 每个执行步骤有 started/succeeded/failed 状态。
- 事件序列可以重放并恢复当前状态。

## 8. 实施阶段（M0-M4）

当前文档方案已完成，以下阶段是尚未实现的代码任务；详细任务、文件范围和验收命令见 [tasks.md](./specs/001-eino-supervisor-template/tasks.md)。

### M0：项目骨架与基础合同

- Go module、typed config、启动校验、Domain 合同、内存 Repository、内存 checkpoint、RunEvent 和 composition。
- 验收：M0 合同测试与 `go test ./...` 通过；当前尚无生产代码，不能把本项写成已完成。

### M1：最小可运行链路

- 固定首条验收链路：`Gin -> Application -> Eino Supervisor -> fake Worker -> fake Tool -> RunEvent -> SSE`。
- 验收：fake 环境的 `POST /api/chat` 在 5 秒内返回唯一终态。

### M2：策略、终态与可靠性

- Policy Gate、Final Guard、审批、幂等、取消、超时、终态和中断恢复。
- 验收：未批准副作用为 0，批准后重复恢复最多执行一次，已完成步骤不重跑。

### M3：真实依赖适配

- 真实 ChatModel、真实 Tool、Callback 和 OpenTelemetry；fake 和零模型合同测试继续保留。
- 验收：真实适配器遵守既有 Domain/Application 合同，凭证不泄漏。

### M4：生产扩展与运营业务

- 持久化 checkpoint、MCP、认证、多租户和具体运营业务模块。
- 验收：每个扩展有需求、ADR、迁移/回退和合同测试；本项不是 M0 前置条件。


## 9. 复用和来源记录

未来如引入来自 Coze 的代码或明显改写，必须在对应实施任务中登记来源、提交、路径、改动和许可证；当前模板不复制外部业务代码，也不把 Coze、Dify 等平台作为运行依赖。

```text
保留 Apache 版权头；修改过的文件增加修改标记。第三方依赖和许可证在引入依赖的任务中同步登记。

## 10. 完成标准

项目完成必须满足：

1. 删除命理代码后可以独立运行。
2. Eino 依赖集中在 `infrastructure/eino` 和 `infrastructure/llm`。
3. Domain 不依赖 Gin、Eino、数据库和 MCP。
4. 新增一个 Worker 不需要修改主执行循环。
5. 副作用动作不能绕过审批和幂等保护。
6. checkpoint 恢复不会重复执行已完成步骤。
7. SSE 只做事件投影，不做业务判断。
8. Coze 来源和许可证可追溯。
9. 本地合同测试、`go test ./...` 和真实 `/api/chat` smoke 通过。
10. 通过配置文件或环境变量可以替换部署参数、模型、Prompt、能力开关和预算；安全合同仍由代码强制。
