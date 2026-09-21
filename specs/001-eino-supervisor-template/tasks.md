---

description: "M0-M4 task list for the Eino Supervisor Template"
---

# Tasks: Eino Supervisor Template

**输入**：`spec.md`、`plan.md`、`research.md`、`data-model.md`、`contracts/`、`quickstart.md` 和项目宪法。

**事实来源**：目录与依赖方向以 [`docs/architecture.md`](../../docs/architecture.md) 为准，当前状态以 [`PROGRESS.md`](../../PROGRESS.md) 为准；本文件是实施任务来源。

**当前边界**：M0-M3 已完成本地合同和可选真实依赖适配；M4 仍是后续生产扩展，不代表持久化、MCP、认证或具体运营业务已经实现。

## M0：项目骨架与基础合同

**目标**：建立可启动、可测试但不连接真实外部系统的 Go 骨架，固定配置、领域合同、内存存储、checkpoint、RunEvent 和 composition 边界。

**独立验收**：`go test ./...` 能覆盖配置非法启动、未知 Worker/Tool 拒绝、状态不变量、内存 Repository/checkpoint 版本和 RunEvent 唯一终态；`go run ./cmd/server/ -f ./config.example.toml` 能启动健康检查。

- [X] T001 [P] 创建 Go module 和约定目录（`go.mod`、`cmd/server/`、`internal/domain/`、`internal/application/`、`internal/interfaces/`、`internal/infrastructure/`、`internal/composition/`）
  - 目标：让模板拥有可编译的最小项目骨架，并遵守 `docs/architecture.md` 的目录边界。
  - 修改文件范围：`go.mod`、`cmd/server/main.go`、上述 `internal/` 目录下的占位实现或包文件。
  - 前置依赖：无。
  - 完成标准：`go list ./...` 成功；没有新增顶层 `workers/`、`tools/`、`state/` 或 `agentflow/`。
  - 验收命令：`go list ./...`。
  - 是否允许并行：是；与 T002、T003、T004 可并行设计，合并前需统一包路径。
  - 不在本任务中：业务 Worker、真实模型、数据库、MCP、认证和 SSE 业务流程。

- [X] T002 [P] 实现 typed config、TOML/环境变量加载和启动校验（`internal/config/`、`config.example.toml`）
  - 目标：固定 `-f 文件 -> TOML -> 环境变量覆盖 -> 启动校验`，让配置只能选择已注册能力。
  - 修改文件范围：`internal/config/*.go`、`config.example.toml`、配置合同测试。
  - 前置依赖：T001 的 Go module。
  - 完成标准：非法地址、超时、预算、未注册实现和越权 allow-list 在启动时失败；密钥不落入样例文件、Prompt、事件或日志。
  - 验收命令：`go test ./internal/config/...`。
  - 是否允许并行：是；不依赖领域合同实现，但提交时依赖 T001。
  - 不在本任务中：热加载、真实模型凭证校验、部署平台和外部服务连接。

- [X] T003 [P] 固定领域合同和状态不变量（`internal/domain/{agent,conversation,operation,approval,tool}/`、`schemas/`）
  - 目标：定义 `SupervisorDecision`、`ApprovedRoute`、`ExecutionPlan`、`WorkerContract`、`ToolContract`、`ApprovalRequest`、`Checkpoint` 和 `RunEvent` 的最小语义。
  - 修改文件范围：`internal/domain/` 合同文件、`schemas/` 结构化 Schema、领域单元测试。
  - 前置依赖：T001。
  - 完成标准：未知能力、非法状态转换、超过步骤/调用/时间预算和不合法终态均确定性拒绝；Domain 不导入 Gin、Eino、数据库、MCP、具体模型或 SSE。
  - 验收命令：`go test ./internal/domain/...`。
  - 是否允许并行：是；可与 T002、T004 并行。
  - 不在本任务中：模型提示词、HTTP handler、真实 Tool 和持久化实现。

- [X] T004 [P] 实现内存 Repository 和内存 checkpoint（`internal/infrastructure/persistence/`、`internal/infrastructure/checkpoint/`）
  - 目标：保存最小会话、运行计划和恢复快照，验证版本递增与已完成步骤不可回写。
  - 修改文件范围：Repository/checkpoint 适配器、并发保护和合同测试。
  - 前置依赖：T001、T003 的接口合同。
  - 完成标准：同一 `run_id` 可读回稳定状态；checkpoint 版本单调递增；终态 resume 只重放原结果，不再次调用步骤。
  - 验收命令：`go test ./internal/infrastructure/persistence/... ./internal/infrastructure/checkpoint/...`。
  - 是否允许并行：是；可与 T002、T003 并行实现适配器。
  - 不在本任务中：磁盘、数据库、Redis、分布式锁和跨进程恢复。

- [X] T005 组装 RunEvent、应用合同和 composition（`internal/application/`、`internal/composition/`、`internal/infrastructure/eventbus/`）
  - 目标：把合同、注册表和内存实现装配成一个可启动的依赖图，保证 Manager 作为运行状态和 `ExecutionPlan` owner。
  - 修改文件范围：Application 接口、RunEvent 记录/投影边界、`internal/composition/*.go`、装配测试。
  - 前置依赖：T002、T003、T004。
  - 完成标准：装配不承载业务判断；每个 run 的事件 `sequence` 单调且最多一个终态；没有绕过 Policy Gate 的 Tool 引用。
  - 验收命令：`go test ./internal/application/... ./internal/composition/... ./internal/infrastructure/eventbus/...`。
  - 是否允许并行：否；需要前置合同和实现全部稳定。
  - 不在本任务中：Gin 路由、Eino Agent、fake Worker/Tool 的完整请求链路。

**M0 检查点**：上述命令和 `go test ./...` 通过后，才允许进入 M1。

## M1：最小可运行链路（P1 / US1）

**目标**：用 fake 能力验证第一条完整入口：`Gin -> Application -> Eino Supervisor -> fake Worker -> fake Tool -> RunEvent -> SSE`。

**独立验收**：本地启动服务后，`POST /api/chat` 对只读请求返回 `started`、决策、计划、进度、文本和唯一 `completed`；未知能力被分类拒绝，整个流程不超过 5 秒。

- [X] T006 [US1] 接入 Gin 请求和 SSE Projection（`internal/interfaces/http/`、`internal/application/chat/`）
  - 目标：将 HTTP 请求映射为 Application 输入，将内部 RunEvent 按顺序投影为 SSE。
  - 修改文件范围：`POST /api/chat` handler、SSE headers/flush、请求校验、公开错误映射和 HTTP 合同测试。
  - 前置依赖：M0 检查点（T005）。
  - 完成标准：传输层不推进业务状态；SSE `sequence` 从 1 开始递增；客户端断开不丢弃已保存 checkpoint。
  - 验收命令：`go test ./internal/interfaces/http/... ./internal/application/chat/...`。
  - 是否允许并行：否；需要 T005 的 Application/RunEvent 合同。
  - 不在本任务中：审批接口、真实模型和真实外部 Tool。

- [X] T007 [US1] 接入 Eino Supervisor、fake Worker 和 fake Tool（`internal/infrastructure/eino/`、`internal/infrastructure/llm/`、`internal/infrastructure/tool/`）
  - 目标：让 Supervisor 只产生候选决策，经 Policy Gate 后由有界 Worker 调用 allow-listed fake Tool。
  - 修改文件范围：Eino 适配器、fake model、`user_analysis` Worker、只读 fake Tool、注册表和适配测试。
  - 前置依赖：M0 检查点（T005）；Policy Gate 的基础 allow-list 可复用 T003 的合同。
  - 完成标准：Worker 不能直接拥有最终答复权；未注册或不在 allow-list 的 Tool 永不执行；结果进入 RunEvent。
  - 验收命令：`go test ./internal/infrastructure/eino/... ./internal/infrastructure/llm/... ./internal/infrastructure/tool/...`。
  - 是否允许并行：是；与 T006 可并行开发不同目录，联调依赖二者完成。
  - 不在本任务中：真实 ChatModel、MCP、业务写入和 OpenTelemetry。

- [X] T008 [US1] 完成 M1 端到端 smoke 和性能合同（`internal/composition/`、`eval/`、`specs/001-eino-supervisor-template/quickstart.md`）
  - 目标：证明首条链路可从真实 HTTP 入口触发并产生唯一终态。
  - 修改文件范围：composition 端到端装配、fake 服务 smoke、性能/事件断言、Quickstart 命令。
  - 前置依赖：T006、T007。
  - 完成标准：`POST /api/chat` 在 fake 环境 5 秒内完成；响应无凭证、Prompt、stack trace 和内部路径；唯一终态为 `completed` 或分类失败。
  - 验收命令：`go test ./...`；运行服务后按 Quickstart 执行 `curl -N -X POST http://127.0.0.1:8080/api/chat ...`。
  - 是否允许并行：否；这是 M1 的集成检查点。
  - 不在本任务中：审批副作用、断点恢复和生产级持久化。

## M2：策略、终态与可靠性（P2/P3 基础）

**目标**：加入 Policy Gate、Final Guard、审批、幂等、取消、超时、终态和中断恢复合同；仍只使用内存和 fake 实现。

**独立验收**：未审批副作用计数为 0；拒绝不调用 Tool；批准后重复恢复 10 次最多执行一次；取消/超时/失败/完成/等待审批可区分；已完成步骤不重跑。

- [X] T009 [US2] 实现 Policy Gate、Tool allow-list 和审批门控（`internal/domain/agent/`、`internal/domain/approval/`、`internal/application/approval/`）
  - 目标：把 Supervisor 候选决策变成确定性的 `ApprovedRoute`，副作用 Tool 未批准不得执行。
  - 修改文件范围：策略合同、Worker/Tool 注册表检查、审批请求与 approve/reject/resume 用例、拒绝测试。
  - 前置依赖：M1 检查点（T008）。
  - 完成标准：模型不能生成审批结果或权限；未知能力、越权风险和未批准副作用均返回分类结果并产生 `approval_required` 或拒绝事件。
  - 验收命令：`go test ./internal/domain/agent/... ./internal/domain/approval/... ./internal/application/approval/...`。
  - 是否允许并行：是；可与 T010 在不同合同文件上并行，联调依赖两者。
  - 不在本任务中：真实审批人目录、认证和多租户授权。

- [X] T010 [US2] 实现 Final Guard、终态和错误分类（`internal/application/run/`、`internal/domain/agent/`、`internal/interfaces/http/`）
  - 目标：统一保护公开结果，确保每个 run 只有一个不可变终态。
  - 修改文件范围：Final Guard、状态机、错误分类、终态事件和脱敏测试。
  - 前置依赖：T003、T005、M1 检查点。
  - 完成标准：`completed`、`failed`、`canceled` 只能出现一个；非法结构化输出、Tool 超时和敏感内容不会被投影为成功答复。
  - 验收命令：`go test ./internal/application/run/... ./internal/interfaces/http/...`。
  - 是否允许并行：是；可与 T009 并行实现。
  - 不在本任务中：全链路观测、持久化审计和真实外部错误协议。

- [X] T011 [US2] 实现幂等、取消、超时和中断恢复（`internal/application/run/`、`internal/infrastructure/checkpoint/`、`internal/infrastructure/tool/`）
  - 目标：让恢复和重试从下一个未完成步骤继续，副作用稳定使用幂等键。
  - 修改文件范围：Manager 执行循环、context deadline/cancel、checkpoint 更新、fake side-effect Tool 和幂等计数测试。
  - 前置依赖：T004、T009、T010。
  - 完成标准：已完成步骤不重复执行；审批等待、取消、超时、失败和终态可区分；重复 resume 10 次的副作用执行次数为 1。
  - 验收命令：`go test ./internal/application/run/... ./internal/infrastructure/checkpoint/... ./internal/infrastructure/tool/...`。
  - 是否允许并行：否；需要 Policy Gate、Final Guard 和 checkpoint 合同。
  - 不在本任务中：进程外持久化恢复、分布式锁和消息队列。

- [X] T012 [US2] 验证 M2 HTTP/SSE 审批与恢复 smoke（`internal/interfaces/http/`、`eval/`、`specs/001-eino-supervisor-template/contracts/http-sse.md`）
  - 目标：从真实入口验证 `approval_required -> approve/reject -> resume` 的事件和状态合同。
  - 修改文件范围：审批/resume handler、SSE 断言、幂等/取消/超时 smoke 和合同文档同步。
  - 前置依赖：T009、T010、T011。
  - 完成标准：审批前无副作用成功事件；拒绝终止；批准后恢复可重复调用且不重复写入；终态事件唯一。
  - 验收命令：`go test ./...`；按 Quickstart 执行审批 smoke。
  - 是否允许并行：否；这是 M2 集成检查点。
  - 不在本任务中：真实外部 Tool、数据库 checkpoint 和认证。

## M3：真实依赖适配

**目标**：在 M0-M2 合同稳定后，增加真实 ChatModel、真实 Tool、Callback 和 OpenTelemetry；fake 路径必须继续可测试。

**独立验收**：在显式配置凭证和外部地址的环境中，真实适配器通过同一 Domain/Application 合同；无凭证时仍可运行零模型合同测试。

- [X] T013 [P] 实现真实 ChatModel 适配和配置开关（`internal/infrastructure/llm/`、`internal/config/`、`docs/technology-baseline.md`）
  - 目标：增加可替换的真实模型实现，保留 fake model 默认路径。
  - 修改文件范围：模型 provider adapter、超时/预算配置、启动校验和依赖说明。
  - 前置依赖：M2 检查点（T012）。
  - 完成标准：真实模型输出仍必须经过结构化解析、注册表和 Policy Gate；凭证不进入事件、日志或 checkpoint。
  - 验收命令：`go test ./...`；在提供测试凭证时运行受限 ChatModel smoke。
  - 是否允许并行：是；可与 T014、T015 并行。
  - 不在本任务中：修改 Domain 合同、开放式自主循环或真实副作用发布。

- [X] T014 [P] 接入真实 Tool 适配器（`internal/infrastructure/tool/`、`internal/infrastructure/`）
  - 目标：在 allow-list、Policy Gate、超时、重试和错误分类不变的前提下接入一个真实只读能力。
  - 修改文件范围：Tool adapter、输入/输出 Schema、sandbox 配置和合同测试。
  - 前置依赖：M2 检查点（T012）。
  - 完成标准：真实 Tool 不能绕过 Worker 声明、Policy Gate、Final Guard 或幂等规则；默认仍使用 fake Tool。
  - 验收命令：`go test ./...`；运行受限真实 Tool smoke。
  - 是否允许并行：是；可与 T013、T015 并行。
  - 不在本任务中：MCP 协议、批量写入、营销渠道和运营业务模块。

- [X] T015 [P] 接入 Callback 和 OpenTelemetry（`internal/infrastructure/observability/`、`internal/composition/`、`config.example.toml`）
  - 目标：记录 run、step、Tool 和模型调用的可关联 Trace/Metric，不改变业务状态 owner。
  - 修改文件范围：Callback adapter、trace/metric exporter、采样配置、脱敏测试和 composition wiring。
  - 前置依赖：M2 检查点；可复用 T013/T014 的调用边界。
  - 完成标准：观测失败不改变终态；敏感字段按最小必要原则脱敏；fake 环境仍能运行。
  - 验收命令：`go test ./...`；按观测配置执行本地 trace smoke。
  - 是否允许并行：是；可与 T013、T014 并行。
  - 不在本任务中：把 OpenTelemetry 变成 Domain 依赖或引入观测平台运行时硬依赖。

## M4：生产扩展与运营业务（后续规格）

**目标**：在真实需求、ADR、合同和指标齐备后，增加持久化 checkpoint、MCP、认证、多租户和具体运营业务模块。

**独立验收**：每个扩展都能证明不改变 Domain 含义、Manager owner、Policy Gate、幂等和 RunEvent 合同，并有迁移/回退验证；本阶段不是 M0 的前置条件。

- [ ] T016 [P] 替换为持久化 checkpoint（`internal/infrastructure/checkpoint/`、`internal/infrastructure/persistence/`、`docs/`）
  - 目标：在不改变 `Checkpoint` 合同的前提下提供跨进程恢复。
  - 修改文件范围：持久化 adapter、迁移/版本策略、故障恢复和重复执行测试。
  - 前置依赖：M3 完成并有持久化 ADR。
  - 完成标准：已完成步骤不重跑；版本兼容、回退和敏感字段最小化有验证证据。
  - 验收命令：`go test ./...`；执行持久化恢复 smoke 和 `go test -race ./...`。
  - 是否允许并行：是；可与 T017、T018 设计并行，但实现前需统一认证与存储边界。
  - 不在本任务中：修改 Domain 所有权、消息队列或无限期历史存储。

- [ ] T017 [P] 增加 MCP Tool 适配（`internal/infrastructure/mcp/`、`internal/infrastructure/tool/`、`docs/`）
  - 目标：把 MCP 作为受控 Tool 来源，不把 MCP 协议泄漏到 Domain。
  - 修改文件范围：MCP client adapter、服务器 allow-list、超时/错误分类、供应链和回退说明。
  - 前置依赖：M3 完成、T014 Tool 合同稳定和安全 ADR。
  - 完成标准：MCP Tool 仍经过注册表、Policy Gate、输入校验和 Final Guard；未授权服务器不可连接。
  - 验收命令：`go test ./...`；运行隔离 MCP sandbox smoke。
  - 是否允许并行：是；可与 T016、T018 并行设计。
  - 不在本任务中：把 MCP 作为 v1 默认依赖或允许动态工具注入。

- [ ] T018 [P] 增加认证与多租户边界（`internal/interfaces/`、`internal/application/`、`internal/domain/`、`docs/`）
  - 目标：为会话、run、审批和 Tool 权限增加租户隔离与身份校验。
  - 修改文件范围：身份适配、租户上下文、授权合同、数据隔离测试和迁移/回退说明。
  - 前置依赖：M3 完成、持久化/安全 ADR；不能以配置直接放宽权限。
  - 完成标准：跨租户读取、审批和 resume 被拒绝；Domain 仍不依赖 HTTP、具体认证 SDK 或数据库。
  - 验收命令：`go test ./...`；执行跨租户拒绝、认证失败和恢复 smoke。
  - 是否允许并行：是；可与 T016、T017 并行设计。
  - 不在本任务中：完整 RBAC 平台、插件市场和外部身份管理后台。

- [ ] T019 增加具体运营业务模块（`internal/domain/operation/`、`internal/application/`、`internal/infrastructure/tool/`、`docs/`）
  - 目标：在已有 Worker/Tool 合同上增加一个有明确边界的运营能力。
  - 修改文件范围：业务领域合同、单一 Worker、所需 Tool、Prompt/Schema、评测样例和验收文档。
  - 前置依赖：M2 合同通过，且 T016-T018 的适用边界已由需求/ADR 确认。
  - 完成标准：新增能力通过注册表接入，不修改主执行循环；副作用仍经过 Policy Gate、审批和幂等。
  - 验收命令：`go test ./...`；运行该业务的真实入口 smoke 和回放测试。
  - 是否允许并行：否；必须先明确业务 owner、数据边界和外部副作用合同。
  - 不在本任务中：命理业务、CRM 全量平台、开放式多 Agent 协商和工作流编辑器。

## 依赖与执行顺序

- M0 无业务前置，但 T001 是其他 M0 任务的共同基础；M0 全部检查点通过后才能开始 M1。
- M1 依赖 M0，按 T006/T007 并行开发、T008 集成验收；它是首个 MVP，可独立演示只读链路。
- M2 依赖 M1，T009/T010 可并行，T011/T012 顺序收口；它补齐策略、终态和恢复可靠性。
- M3 依赖 M2；T013、T014、T015 可并行，但均不得改变既有合同。
- M4 依赖明确需求和 ADR，T016-T018 可并行设计；T019 只有在业务边界确定后实施。

## 并行执行示例

```text
M0: T002 配置  || T003 Domain 合同 || T004 内存实现 -> T005 composition
M1: T006 Gin/SSE || T007 Eino/fake 能力 -> T008 端到端 smoke
M2: T009 Policy Gate || T010 Final Guard -> T011 可靠性 -> T012 smoke
M3: T013 ChatModel || T014 Tool || T015 Callback/OTel
M4: T016 checkpoint || T017 MCP || T018 认证/多租户 -> T019 业务模块
```

## 实施策略

M0-M3 已按合同逐步收口；M3 仍保留 fake、内存和零模型合同测试作为默认本地门禁。M4 只有在真实需求、ADR 和外部依赖边界明确后再实施。
