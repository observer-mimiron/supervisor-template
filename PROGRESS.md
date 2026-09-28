# Progress

## 当前阶段

- M0-M3 已实施完成；`config.example.toml` 默认 DeepSeek 模型，存储仍默认内存、示例业务 Tool 仍为 fake；离线 HTTP smoke 显式使用 `-fake`。
- 运行链路为 `HTTP/SSE + AuthN -> Application + resource AuthZ -> Supervisor -> Policy Gate -> Manager -> WorkerRunner -> Tool -> Final Guard -> Event`。
- M4 本轮已实施持久化 checkpoint、受控 MCP Tool 和最小认证/资源隔离边界；示例业务模块已完成集中注册。真实运营业务及完整 OIDC/JWT、RBAC、多租户平台延期。

## 已验证事实

- 已建立 Go module、`cmd/server/` 和 `internal/{domain,application,interfaces,infrastructure,composition}/`。
- 配置按 `TOML -> 环境变量 -> Validate` 加载；Supervisor 的 `allowed_workers`、Worker 的 Tool 白名单和实现标识均启动时校验。
- 服务样例默认 `deepseek-chat`，API key 由 `LLM_API_KEY` 提供；`-fake` 仅用于离线 smoke，启动日志记录生效 provider。
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
- 文件 Repository/checkpoint/event 使用版本化 JSON、哈希 run_id 路径和临时文件 rename；第二个实例可读取请求、审批、计划、checkpoint 和事件。
- MCP 只从启动配置的 server/tool allow-list 调用，具备输入校验、1 MiB 响应上限、请求大小上限以及 timeout/unavailable/protocol/business 分类；默认 fake/HTTP 配置不变。
- `/api/*` 先要求 Bearer 凭证；认证器只比较环境变量中的 token SHA-256，并生成 `tenant_id + subject_id` 主体；`/healthz` 保持公开。
- run 创建时绑定认证主体；重放、审批、resume 和 cancel 通过独立 `RunAuthorizer` 校验所有权，审批人取认证主体而非客户端字段；跨主体操作不会改变原 run。
- `PolicyEvaluator` 仍只管理模型提出的 Worker/Tool 动作权限，未与用户资源授权混用。
- C001-C018 组件化基础批次已完成：启动期 `RuntimeCatalog` 冻结配置快照，应用层通过 `WorkerRunner` 执行，默认 `SingleToolRunner` 保持单 Tool 行为；重复注册/缺失引用/allow-list/冻结快照、Runner 取消、未知结果、事件修复、审批审计和示例业务注册均有合同测试。
- C018 集中示例业务模块已完成：`examplebusiness` 统一声明 Worker、Route、Prompt、Tool 绑定和安全合同；Tool/LLM/Composition 只消费注册描述，未修改 `run.Service`、HTTP、Policy 或 SSE。
- C019-C023 收敛缺口已完成：未匹配请求不再 fallback 到只读 Tool；运行时执行计划使用步骤、调用、重试、成本和 deadline 预算；注册 Tool 统一校验必填输入、UTF-8/控制字符/大小输出；真实模型只接受完整 JSON；取消通过 per-run context signal 协作推进且 Runner 调用期间不持有全局状态锁。

## M5-M8 审计进展（2026-09-24）

- 修复 Runner 合同包归属：`RunnerSession` 由 `internal/application` 拥有，`internal/application/run` 保留别名；修正 Eino 包中的 Runner 合同断言。`go test ./internal/application/run ./internal/infrastructure/eino` 通过。
- C027 补充故障注入：running checkpoint 写入失败时 Runner 不会启动；结果计划持久化失败时进入 `waiting_reconciliation` 且 Resume 不重试；成功进度/文本投影缺失时 Resume 补投影且终态唯一。`go test ./internal/application/run` 通过。
- Tool Pool、Registry、HTTP/MCP 错误分类与 Memory 实现未重写；对应本地合同测试通过。`go test ./internal/infrastructure/tool ./internal/infrastructure/mcp ./internal/application/memory ./internal/infrastructure/memory ./internal/infrastructure/checkpoint ./internal/infrastructure/eventbus` 和 `go test -race ./internal/infrastructure/tool ./internal/infrastructure/memory ./internal/application/memory` 通过。
- Eino `ResumeToken` 现在调用 ADK `Resume`；函数型和 Eino WorkerRunner 有共享断言，覆盖已批准步骤、结果/checkpoint、恢复、取消、deadline 和 unknown outcome。Composition 现在通过 `RunnerFactory -> WorkerRunnerDispatcher` 按 Worker 配置装配 `single_tool` 或 `eino_adk`，并由 `AgentFactory` 把 `ApprovedTool` 注入 ADK `ToolsConfig`，Eino ToolCall 进入 ADK ToolNode 后再走共享 Registry/Pool。
- 真实入口 smoke：本地 `go run ./cmd/server/ -f ./config.example.toml` 以 `LISTEN_ADDR=:18080` 启动；`/healthz`、只读 `/api/chat`、审批后 resume 和待审批 run cancel 均返回 HTTP 200。只读 SSE 顺序为 started/decision/plan/progress/tool_call/progress/text/completed；批准后完成，待审批取消产生 canceled。18080 服务已停止，既有 8080 服务未触碰。
- 全量验证通过：`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`go vet ./...`、`git diff --check`。
- C024-C038、C040-C041 的对应代码/测试/文档检查通过后已更新任务状态。C039 的应用 checkpoint 传递、Runner 工厂装配和 ADK ToolNode 路径已实现；本地 DeepSeek 兼容 HTTP stub 补充了真实 Eino 适配器的 ToolCall -> Registry/Pool -> HTTP/SSE 审批闭环证据。真实 DeepSeek Supervisor 的只读 `/api/chat` smoke 已通过；真实模型驱动的 Eino Worker ToolCall 和跨进程 Eino checkpoint 重放仍未验证，因此 C039/C042 的生产级范围继续保持 partial/deferred。

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

## 示例与 Eino 增量（2026-09-25）

- 已完成单 Worker 与串行两步多 Worker 示例：`user_analysis -> user_query`，以及
  `user_analysis/user_query -> user_summary/user_summary_query`。两步候选由 fake/real Supervisor
  以有限 `steps` 表达，Application 对每步重新执行 Policy Gate，最多两步，按 `ExecutionPlan`
  顺序推进；第二步只接收有界的第一步结果。
- 已完成本地 Eino ToolCall 证据：确定性 `model.ToolCallingChatModel` 生成一个 ToolCall，经过
  ADK ToolNode、`ApprovedTool` 和共享 Tool Executor/Registry/Pool；固定输入、单步调用预算和
  checkpoint token 均有测试，未使用网络凭证。
- 已补 Eino side-effect 证据：批准 Tool 构造前不会写入；批准后首次调用成功；重复 Eino step
  使用相同幂等键时底层模拟写入计数保持为 1。Composition 也覆盖了 `eino_adk` Runner 的完整
  模型、Tool、校验器和 checkpoint 装配。
- 已完成组合装配拒绝合同：缺少 ToolCallingChatModel、checkpoint 或未注册 Worker Runner 会在
  composition 启动/构建阶段失败。
- 2026-09-25 新鲜验证通过：`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、
  `go vet ./...`、`git diff --check`；本地 `:18082` 的 `/healthz`、单 Worker `/api/chat`、两步
  串行 `/api/chat` 均通过，SSE 顺序和唯一 `completed` 已核验；新增
  `TestEinoSelectedHTTPApprovalAndResumeStayApplicationOwned` 通过。
- 真实 DeepSeek HTTP smoke 已完成一条只读 `/api/chat`：Supervisor 由真实 `deepseek-flash`
  选择 `user_analysis -> user_query`，后续 Worker/Tool 使用本地 fake；SSE 到唯一 `completed`，未访问外部业务系统。
- 保持未完成：真实模型驱动的 Eino Worker ToolCall、跨进程 Eino resume，以及多轮 ReAct/并行/DAG。
  Eino 选中 runner 的本地 HTTP 审批/幂等/拒绝闭环另由兼容 HTTP stub 集成测试覆盖。

- 本次真实模型 smoke 使用本机 `.env` 的 DeepSeek 凭证；没有使用外部 Tool 地址或执行真实业务写入。
- 本地 HTTP stub 另验证了官方适配器的 Eino Worker ToolCall、应用审批和 Registry/Pool 链路。
- 沉睡客户召回分析已实现：固定合成 fixture、只读客群筛选、两步有界汇总、独立审批/幂等模拟触达和中断恢复均通过本地合同测试；真实 CRM/触达仍未接入。
- 静态 Bearer 认证只适合本地/受控环境；完整 OIDC/JWT、RBAC、组织层级、动态主体目录和真实运营业务仍未实施。
- 文件持久化不承诺数据库级高可用、跨主机并发写入或无限期历史存储；MCP 未做动态发现和写入型 Tool。
- 真实运营业务、完整 Eino 多步 ReAct/Graph Runner、动态插件和通用平台化能力仍未实施；当前默认 Runner 是最小单 Tool 适配器。历史演进约束见 `docs/scaffold-evolution-plan.md`，当前待执行计划以 `specs/001-eino-supervisor-template/plan.md` 和 `tasks.md` 为准。
- 当前不宣称跨进程 exactly-once 或强制杀停不响应 context 的外部进程；外部调用后结果提交前中断时返回 `RUN_OUTCOME_UNKNOWN`，不自动重试。
- M5-M7 合同审计测试和 HTTP/SSE smoke 已通过；M8 完成 Eino adapter 层 ResumeToken dispatch、shared WorkerRunner 基础合同和本地 HTTP ToolCall/Pool 审批闭环。跨进程 Eino 应用级恢复、真实模型驱动的 Eino Worker ToolCall 仍未完成（C039/C042 partial/deferred）；真实模型 Supervisor 的只读 `/api/chat` 已通过。

## M9-M12 合并计划实施进展（2026-09-27）

- `speckit-implement` checklist gate 已完成需求质量审阅；四份 checklist 共 122 项均已勾选，勾选表示需求审阅结论，不表示实现完成。
- M9 初始基线发现 `config.example.toml` 含文件观测/Langfuse 字段但配置结构未定义，已补齐对应配置字段；此前阻断的 `config`、`composition`、HTTP 测试恢复通过。
- 已增加 HTTP W3C trace context 提取、`X-Trace-ID`/`X-Request-ID` 响应头和 SSE trace 关联。没有安装 OTel SDK 时也生成请求关联 ID。该 ID 只保证单个 HTTP 请求内关联；跨 resume 使用 `run_id` 关联，未宣称同一 trace 跨请求。
- 已增加 `slog` JSON 日志工具、低基数字段上下文、敏感关键字/常见内部路径脱敏及单进程有界轮转文件 writer；`composition.App` 现在创建并关闭配置的日志 writer，启动日志使用该 logger。trace snapshot、完整 degraded signal 和业务层统一诊断字段仍未完成。
- T004 的 context-aware EventStore/Checkpoint 路径已完成，事件观测包装器会把子 span context 传到底层 context-aware store，并有合同测试；配置 endpoint、Langfuse headers、resource attributes 和观测文件启动校验已补齐，但完整 Langfuse exporter 语义和 trace snapshot 仍未完成。
- 当前完成任务：T001-T004、T014。`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`go vet ./...`、`git diff --check` 已通过；Compose 结构校验需提供本地 MySQL 密码变量后通过。MySQL/GORM 和真实外部观测后端尚未实现/验证。
- Compose 方向已明确：Go 服务由宿主机 `go run ./cmd/server` 启动，Compose 只启动 MySQL，Collector 通过 `observability` profile 可选启动；MySQL schema/seed、数据库运行链路 smoke 及 Langfuse exporter 仍未完成，T030 不勾选。

## M9-M12 最新收口（2026-09-28）

- 已清理 `internal/composition/debug_tmp_test.go` 和 `internal/application/run/service.go` 的临时 DEBUG 输出；新增 `TestNewFakeOutreachKeepsAudienceProjectionThroughResume`，覆盖真实 composition 装配后的审批恢复。
- 已确认 fake payload 回归问题的根因是仓库 `.env` 中 `MODEL_PROVIDER=deepseek` 覆盖了 `config.example.toml` 的 fake provider；显式 `MODEL_PROVIDER=fake` 后，真实 HTTP 链路中的 `simulated_outreach` 收到 audience projection，批准后成功。
- 默认 DeepSeek 模型也完成新端口 `18107` 的触达回归：Supervisor 生成 `simulated_outreach` 审批计划，批准前 SSE 未出现 `tool_call`；批准后 resume 到唯一 `completed`，文本为“已模拟触达匿名用户：4”。重复 resume 只重放原有 9 个事件、单个 `tool_call` 和单个终态；此次调用使用本地模拟 Tool，不是外部业务写入。
- 新端口 `18095` 已实际验证 `/healthz`、只读 `/api/chat`、两步串行、模拟触达审批前无执行、批准后 resume、重复 resume、reject 和 cancel；SSE 均保持唯一终态。
- MySQL healthy 容器和宿主机 Go 服务新端口 `18096` 已实际验证订单查询、审批插入和重复 resume；重复 resume 返回同一 `order_id=3`，未新增订单。
- 对应任务账本见 `specs/001-eino-supervisor-template/tasks.md`；M9-M11 链路收口后已单独完成 Langfuse 多角度 trace 评测并汇报，不能用配置或 exporter 初始化代替评测证据。
- 最新门禁已实际通过：`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`go vet ./...`、`git diff --check`。
- 修复 OTLP base endpoint 未追加 signal path 的问题：`http://localhost:3001/api/public/otel` 现在正确导出到 `/v1/traces` 和 `/v1/metrics`，并有 `normalizeOTLPEndpoint` 单元测试。
- M9-M11 链路收口后完成 Langfuse 多角度运行评测；只读、两步串行、审批恢复和重复 resume 均在 Langfuse 项目 `agent-runtime` 中产生可检索 trace。评测请求、trace ID、结构性评分、失败分类和前置条件见 `docs/langfuse-evaluation-20260928.md`。
- 配置默认现已与运行意图对齐：`config.example.toml` 显式使用 DeepSeek；离线 Case Runner 和 fake smoke 单独强制本地 fake，避免把环境覆盖误读成项目默认值。

## Convergence 评测闭环（2026-09-28）

- `cmd/eval` 现在在写入 JSON/JSONL 报告后同步打印可读摘要：Case 总数、通过/失败数、终态和 fake 写入次数；便于现场展示，机器报告仍是 CI 事实来源。
- T035-T037、T045 已实现：`eval/` 提供严格 Dataset loader（含动态/secret-like/URL 拒绝）、正向/负向/边界/多样性八 Case、真实本地 HTTP/SSE Runner（仅 pre-call 安全失败重试，unknown outcome 不重试）、四维确定性 Evaluator 和 `cmd/eval` JSON/JSONL 报告；高风险全量实际结果为 `passed=8 failed=0`，JSONL 输出为 9 行（envelope + 8 Case），低风险 `audience-query` 选择实际结果为 `passed=3 failed=0`。
- T038 已实现：`eval/feedback.go` 提供 PR 风险、失败 taxonomy、人工 disposition、Judge/rubric 合同和脱敏 digest；Judge 仍是 advisory，不改变硬门禁。
- T039 已实现：`cmd/archcheck` 实际输出 `package dependency direction OK`；`.github/workflows/ci.yml` 接入 archcheck、Go 测试、评测测试和本地 Case Runner。Required Checks/Reviewers 仍需托管平台设置，未将 workflow 存在误报成门禁已生效。
- T040-T042 已实现并有测试：Case 元数据通过低基数 HTTP span attributes 关联；HTTP/Run/Model/Tool/Event 有结构化观测和指标；exporter/log/snapshot 故障产生一次 degraded signal；Trace snapshot 具备 0600、脱敏、大小/日期轮转、保留和重启读取。
- T043 已实现：`mysql_otel_test.go` 使用标准库 fake SQL driver，实际验证 GORM query/insert span 到达本地 provider，且不包含 SQL 参数或用户 payload。
- T044 已实现：新增 `docs/evaluation-method.md`，并更新 quickstart、任务账本和本段证据说明。
- T046-T049 已实现：Runner 通过组合层窄端口记录 fake write count、cleanup result、启动注册 Tool/Worker 快照；side-effect evaluator 基于实际写入和清理证据断言审批前无写入、重复恢复至多一次；CLI/CI 接入 risk/impact-tag 选择；architecture evaluator 使用启动快照与 decision/plan Policy 证据；Case 报告关联结构化 `failed_assertion`、failure taxonomy 和 nullable human disposition。
- 本轮最新复验通过：高风险本地 Case `passed=8 failed=0`；`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`go vet ./...`、`go run ./cmd/archcheck`、`git diff --check` 均通过。
- Langfuse 仍按 M12 单独报告；本轮本地评测不把 OTLP/Langfuse 配置或 exporter 初始化当作在线 Judge/多角度评测证据。
- T055 收敛文档任务已完成：`plan.md` 的范围和限制已对齐 M9-M12 运行链路及评测现状。当前 DeepSeek 默认模型触达回归见上文；工作区未发现临时调试文件或 `DEBUG fmt.Printf`。

## 关键约束

- `.specify/memory/constitution.md` 未修改，仍是项目唯一宪法。
- Domain 不依赖 Gin、Eino、数据库、MCP、具体模型或 SSE。
- 公开事件和错误不包含凭证、原始 Prompt、stack trace 或内部文件路径。
- 任何新增副作用仍需 `Policy Gate -> Approval -> Idempotency -> Tool -> Audit Event`。
- 组件注册必须在启动期由 `composition` 显式完成；禁止反射扫描、`init()` 自注册、热加载和万能 `Component` 接口。
