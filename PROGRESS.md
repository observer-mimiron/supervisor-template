# Progress

> 本地 Spec Kit 工作区 `specs/`（Feature Spec、Plan、Tasks 等）不随模板发布；`.specify/` 只发布
> 唯一宪法 [`memory/constitution.md`](.specify/memory/constitution.md)，其余是机器本地状态。
> 下文出现的 `specs/...` 路径是编写时的证据指针，在仓库里不可见。

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
- 对应任务账本见 `specs/001-eino-supervisor-template/tasks.md`；M9-M11 链路收口后已单独完成 Langfuse 多角度 trace 评测并记录，不能用配置或 exporter 初始化代替评测证据。
- 最新门禁已实际通过：`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`go vet ./...`、`git diff --check`。
- 修复 OTLP base endpoint 未追加 signal path 的问题：`http://localhost:3001/api/public/otel` 现在正确导出到 `/v1/traces` 和 `/v1/metrics`，并有 `normalizeOTLPEndpoint` 单元测试。
- M9-M11 链路收口后完成 Langfuse 多角度运行评测；只读、两步串行、审批恢复和重复 resume 均在 Langfuse 项目 `agent-runtime` 中产生可检索 trace。评测请求、trace ID、结构性评分、失败分类和前置条件见 `docs/langfuse-evaluation-20260928.md`。
- 配置默认现已与运行意图对齐：`config.example.toml` 显式使用 DeepSeek；离线 Case Runner 和 fake smoke 单独强制本地 fake，避免把环境覆盖误读成项目默认值。

## Convergence 评测闭环（2026-09-28）

- `cmd/eval` 现在在写入 JSON/JSONL 报告后同步打印可读摘要：Case 总数、通过/失败数、终态和 fake 写入次数；便于快速查看，机器报告仍是 CI 事实来源。
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

## Durable Execution Closure（2026-09-29）

- `implemented`：新增 `RunLease`/`RunLeaseStore` 应用合同；memory、file 和现有 GORM/MySQL adapter 支持 owner token、expiry、claim、ownership check 和 owner-checked release。composition 已按 memory/file/MySQL 选择并注入租约，健康检查拒绝缺少租约实现的装配。
- `implemented`：`Start`、`Approve`、`Resume`、`Cancel` 均先 claim Run lease，Repository/Checkpoint/EventStore 写入前检查 ownership，所有退出路径释放 lease。file lease 使用本地 `flock`，锁等待响应 context cancel；不宣称跨主机共享文件协调。
- `implemented`：Plan/Step 快照保存 `Attempts`、`AttemptStatus`、`ErrorClass`、`UpdatedAt` 和 terminal 状态。Runner 前的 `prepared` 标记可在恢复时回到 pending；已进入 `running` 的快照在 lease 接管时转为 `waiting_reconciliation`，不会自动重试未知副作用。
- `implemented`：新增竞争 claim、expiry takeover、文件重启、终态事件追加失败修复和服务级 lease 测试；已有 unknown outcome、重复 resume、事件序号/唯一终态、checkpoint/plan 部分失败测试继续通过。
- `partial`：Plan、Checkpoint、EventStore 是独立存储边界，当前是可恢复的 at-least-once projection，不是跨存储 exactly-once；lease renewal、数据库事务/outbox 未实现。可选 GORM/MySQL adapter 不承载完整 Runtime 状态，多实例恢复未验证且不是当前单实例目标的要求。
- `deferred`：Temporal/LangGraph 服务端语义、队列/DAG、跨主机 file lease、外部副作用 exactly-once、强制终止不响应 context 的进程。

本轮真实验证：`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`go vet ./...`、`go run ./cmd/archcheck`、指定 enterprise eval（8 cases，8 passed/0 failed）和 `git diff --check` 均通过。Eval 终态为 `completed`、`failed`、空（invalid input case）或 `canceled`，fake write count 分别在每个 Case 摘要中输出。

## Evaluation Evidence Contract（2026-09-29）

- `implemented`：`RuntimeSnapshot`/Runner 现在记录注册 Tool 的 risk、requires-approval 和 idempotency 元数据；side-effect evaluator 按合同判断审批前置，不再硬编码 `simulated_outreach`。
- `implemented`：Dataset loader 要求每个实际 Case 声明四个硬评测维度，并按正常 Run、Tool、审批和 request error 路径校验最小 evidence matrix；Evaluator 固定执行四个维度，Case 声明只作为不可关闭的合同校验，缺失声明或通用必需证据会硬失败。
- `implemented`：Evaluator 增加统一 evidence integrity 校验：Evidence 必须关联非空且匹配的 Case/版本/Run，事件必须带非空且唯一的 EventID、匹配 Event RunID/首个 trace，sequence 从 1 连续递增，Terminal 与唯一 terminal event 一致，且 Policy/Plan 证据必须先于 `tool_call`。新增反绕过测试覆盖缺失 EventID、缺失维度/证据、伪造 terminal、重复 EventID、RunID/trace mismatch、Policy 乱序和匿名 side-effect；非法 risk/tag 选择不会生成空的假阳性报告。
- `deferred`：MySQL lease 集成测试提供 `MYSQL_TEST_DSN` 可选入口；本轮因未设置 DSN 跳过。真实 MySQL 多实例恢复不在当前单实例目标范围内；若部署需求改变，再在真实目标数据库上验证。MySQL 示例订单 Tool 与 lease adapter 继续复用 GORM，但 Repository/Checkpoint/EventStore 仍使用 memory/file，不是完整 MySQL Runtime 持久化。

最新本地评测命令：`go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v1.json -report ./tmp/eval-report-enterprise-v2.json -code-version enterprise-execution-v1 -config ./config.example.toml`，8 cases，8 passed / 0 failed；每个 Case 的 terminal 和 fake write count 已打印。

本轮证据合同收口后的复验仍通过：`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`go vet ./...`、`go run ./cmd/archcheck`、指定 enterprise eval（8 cases，8 passed / 0 failed）和 `git diff --check`。`go test -v ./internal/infrastructure/persistence/mysql` 显示 fake GORM tracing/输入合同通过，`TestMySQLRunLeaseContract` 因未设置 `MYSQL_TEST_DSN` 跳过。该真实 MySQL 多实例恢复证据当前 deferred，因单实例部署目标不需要，不作为本轮门禁；MySQL adapter 不是完整 Runtime Repository/Checkpoint/EventStore。

## Evaluation Anti-bypass Closure（2026-09-29）

- `implemented`：side-effect evaluator 现在要求 fake write 至少有一个注册且声明 `Risk=side_effect` 的 Tool call；只读 Tool call 不能为写入提供归因。
- `implemented`：审批证据按完整 `step_id + worker_id + tool_id` 绑定，不能用同一步的其他 Worker 或 Tool 借用批准；新增同一步不同 Worker、只读写入和重复 side-effect call 合同测试。
- `implemented`：同一绑定键重复产生 side-effect `tool_call` 时硬失败，保留 `tool_contract` 等更具体的首个失败分类。

本轮增量及完整验证均通过：`go test ./eval/evaluator`、`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`go vet ./...`、`go run ./cmd/archcheck`、enterprise eval（8 cases，8 passed / 0 failed）和 `git diff --check`。

## 关键约束

- `.specify/memory/constitution.md` 未修改，仍是项目唯一宪法。
- Domain 不依赖 Gin、Eino、数据库、MCP、具体模型或 SSE。
- 公开事件和错误不包含凭证、原始 Prompt、stack trace 或内部文件路径。
- 任何新增副作用仍需 `Policy Gate -> Approval -> Idempotency -> Tool -> Audit Event`。
- 组件注册必须在启动期由 `composition` 显式完成；禁止反射扫描、`init()` 自注册、热加载和万能 `Component` 接口。

## Clean Architecture P1 收口（2026-09-30）

- `implemented`：PolicyGate allow-list 私有化并深拷贝；运行服务删除裸 Tool fallback，健康检查要求已装配 WorkerRunner，测试显式使用 SingleToolRunner/dispatcher。
- `implemented`：新增 `RegistrationSnapshot` 与 `CompileRuntimeCatalog` 注册快照校验；examplebusiness 保持唯一业务注册源，配置不再硬编码业务 ID 白名单。
- `implemented`：Tool Registry 仅接收外部合同和 handler；fake runtime、fake decision payload、HTTP/MCP/MySQL handler 均由 composition/业务模块注入。
- `implemented`：auth、llm、observability 改用基础设施本地 Options；archcheck 覆盖 cmd/application、interfaces/tool、infrastructure/config，并保留 observability tracing 例外。
- `implemented`：MySQL 拆分共享 Connection、RunLeaseAdapter、OrderToolAdapter；App.Close 使用 `errors.Join` 并只关闭共享连接一次。
- `verified`：`go test ./...` 已通过；其余 race/build/vet/archcheck/smoke 在本轮收口阶段继续执行，未执行前不标记为通过。

## Evaluation Postcondition Closure（2026-09-30）

- `implemented`：Case 可选声明 `postconditions.database`，Runner 在请求前后通过已装配的 MySQL/GORM Adapter 读取订单状态；报告只保留行数和规范化字段摘要哈希，Evaluator 对 JSON 结果与最终数据库状态分别判定。
- `implemented`：Case 可选声明 `postconditions.logs`，Runner 捕获本次本地运行的 JSONL `slog`，仅投影 `phase`、`error_code` 等低基数字段并拒绝敏感字段；GORM logger 保留错误/慢查询但启用参数化输出。
- `implemented`：GORM 查询/写入继续通过 OTel Database Span 关联 Run Trace；Langfuse 只接收结构化 Trace/Observation 和可选 Boolean Score，不接收原始 SQL 文本或参数。
- `verified`：数据库 Snapshot、日志脱敏、后置断言、`go test -race ./...`、`go vet ./...`、`go run ./cmd/archcheck`、本地 8 Case（8 passed/0 failed）均已通过；无 Langfuse 凭证时 `-langfuse-upload` 只 warning，不改变本地退出码。
- `partial`：真实 MySQL/Langfuse 已有本机 smoke 证据，但默认 PR 仍不依赖外部数据库、Langfuse 凭证或网络；生产目标环境仍需单独配置和复验。

## Real MySQL/Langfuse Smoke（2026-09-30）

- `verified`：使用本机 MySQL 容器和临时配置运行真实 `mysql_order_insert` Case，fake Supervisor 经审批/resume 完成订单写入；JSON、事件序列、数据库前后计数/字段摘要和日志后置断言均通过（1/1）。测试前订单数为 2，写入后为 3，随后按本次新增 ID 清理并恢复为 2。
- `verified`：使用本机 Langfuse OTLP 和 Public API 凭证完成真实观测 smoke；Score API 成功上传 6 个 evaluator scores。Langfuse 异步摄取后可查询 Trace，resume Trace 包含 `insert orders` GORM Observation，`db.query.text` 只含 `?` 参数占位符，无业务值。
- `note`：Trace 在进程结束后立即查询可能返回 404；等待异步摄取后查询返回 200。该延迟不影响本地确定性评测结果。

## Feature 002 收口：Project Feature Acceptance Evaluation（2026-10-01）

- `implemented`：`eval.EvaluationProfile` 定义四种场景（`runtime-fake`、`runtime-real`、`coding-fake`、`coding-real`）及 plane/provider/executor/verifier/network/tier 跨字段校验；JSON 报告与 JSONL envelope 都写入不可变 `evaluation_profile`，Evaluator 只读取、不推断。
- `implemented`：`cmd/eval` 的本地 Runtime Runner 无条件强制 fake。实际以 `MODEL_PROVIDER=deepseek` 运行，报告仍为 `runtime-fake`；CLI 合同测试固定该行为与 profile/报告一致性。
- `implemented`：本地 Runner 只接受 `runtime-fake` + `local-fake` + `fake`。`Runner.ValidateProfile` 在运行 Case 和写报告之前校验，不匹配（如 `runtime-real`、`coding-fake`、空 profile）以退出码 `2` 返回 `evaluation_setup_error` 且不生成报告文件。
- `implemented`：Executor 合同测试覆盖 Case timeout、pre-call retry budget（含“已到达服务端不重试”）、cleanup 结果和 executor/profile 不匹配；evidence 边界测试断言报告只保留有界字段（DatabaseEvidence/DiagnosticLogEvidence 字段集固定），且不含凭证、完整 Prompt、SQL 参数或主机绝对路径。
- `implemented`：`eval/datasets/feature-acceptance-template.json` 现在可被 `eval.Load` 加载，并在本地 fake 链路通过（1/1），可直接复制为新功能 Case 的基线。此前该模板因 `category` 与 `cleanup_policy` 非法而无法加载。
- `implemented`：Loader 新增 12 项非法合同用例（缺失 dataset/case 版本、重复 id@version、缺失终态期望、空请求步骤、非正/缺失 timeout、负 retry budget、空幂等键、非法 cleanup policy、非法 category），并新增 `TestLoadShippedFeatureAcceptanceTemplate` 守护模板本身可加载。
- `implemented`：CLI 摘要行显式打印 `scenario=`，避免本地 fake 结果被读成真实模型能力；`cmd/eval` 的 `run()` 已可从测试调用，覆盖 fake 强制、setup-error 退出与报告一致性。
- `implemented`：Langfuse 仍为可选 sink。凭证缺失打印 `langfuse score upload skipped:`，HTTP 失败打印 `langfuse score upload warning:`；两者都不改变退出码、通过/失败判定和 `runtime-fake` profile。
- `partial`：`runtime-real` 是 profile/合同边界而非可执行操作——仓库内没有任何命令能产出 `runtime-real` 报告，真实模型能力不能由 `runtime-fake` 报告推断。该边界已在 quickstart、`docs/evaluation-method.md`、README 和 `contracts/executor.md` 中显式写明，不再暗示存在可运行命令。
- `deferred`：`coding-fake` 与 `coding-real` 只有 profile/executor 合同边界，未实现隔离执行器与独立 verifier；Langfuse 托管 Dataset/Experiment 同步和在线 LLM Judge 未实现，本轮未执行任何在线评测或 Judge 判定。
- `verified`（2026-10-01 实际执行）：`go test ./...`、`go test -race ./...`、`go test ./eval/...`、`go test ./cmd/eval/`、`go build ./cmd/server/`、`go vet ./...`、`go run ./cmd/archcheck`、`gofmt -l cmd internal eval`（无输出）和 `git diff --check` 均通过。
- `evidence`：`go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v1.json -report ./tmp/eval-report.json -code-version dev -config ./config.example.toml` 实际结果为 8 cases / 8 passed / 0 failed，退出码 0，摘要为 `scenario=runtime-fake`；报告 `evaluation_profile` 为 `runtime` / `runtime-fake` / `fake` / `fake-supervisor@1` / `local-fake` / `deterministic` / `disabled` / `pr`。模板命令 `-dataset ./eval/datasets/feature-acceptance-template.json` 实际结果为 1/1 passed。
- `evidence`：同一 Dataset 连续独立运行三次，8 个 Case 的 `verdict_digest`、终态和通过判定完全一致（三次运行的首个 `trace_id` 互不相同，说明摘要差异确实只来自按请求生成的 trace）。
- `implemented`：三个评测包补齐包级责任说明（`eval`、`eval/evaluator`、`eval/runner`），各自写明所属层、负责内容和明确不负责的内容（不拥有 Runtime 状态、Policy、Approval、Idempotency 和终态事件），与 `internal/` 既有约定一致。
- `implemented`：`verdict_digest` 不再包含按请求随机生成的 `trace_id`，同一 fake Case 在多次独立运行中因此产生相同摘要；Trace 关联仍保留在 Evidence 中用于 Langfuse score 链接。新增 CLI 级确定性测试：同一 Case 独立运行两次，终态、逐 evaluator 判定、fake write count、cleanup 和 `verdict_digest` 全部一致。

## Langfuse sink 内容边界修复（2026-10-01）

- `fact`：本机**实际运行着**自托管 Langfuse 3：`langfuse-langfuse-web-1`（`0.0.0.0:3001->3000`，Up 2 weeks）、`-worker-1`、`-clickhouse-1`、`-minio-1`、`-db-1`、`-redis-1`，`GET http://localhost:3001/api/public/health` 返回 200，项目为 `agent-runtime`。此前只按 2026-09-28 的文档记录推断而未核实当前状态，此处补齐事实。
- `defect`（HIGH，已修复）：`business_correctness` 在业务断言失败时用 `%v` 把 `evidence.Result` 拼进 `Result.Reason`，而 `eval/langfuse` 把该文本原样作为 score 的 `comment` 上传，于是 Tool 结果投影（含 `customer_ids`）被写进 Langfuse score 记录，违反 FR-008「集成 MUST NOT 接收 tool payloads」。这是结构问题：是否合规取决于每个 evaluator 是否自觉不把 evidence 插进 `Reason`，而不是由类型或边界保证。
- `reproduced`：对真实例运行 `cmd/eval -langfuse-upload`（临时配置指向 `http://localhost:3001/api/public/otel`，临时数据集把 `positive-audience-query` 的期望 `count` 改成 999 以触发失败），从 Langfuse Public API 读回 `code_version=leak-check` 的 `business_correctness` score，其 `comment` 含 `result=map[count:999 customer_ids:[cust-001 cust-002 cust-006 cust-008] spend_365d_total:6200]`。
- `fix`：`eval/langfuse` 不再转发 `Result.Reason`。新增 `scoreComment`，只从封闭字段拼 `comment`（`failed_assertion=<x>` + `taxonomy=<y>`，通过的 Score 不带 comment）；metadata 键集保持封闭。本地 JSON 报告的 `Reason` 不变，排障信息不丢失。
- `verified`：用同一真实例复验，`code_version=leak-fixed` 的 scores 中 `business_correctness` 的 comment 为 `failed_assertion=result taxonomy=business_expectation`，对 `cust-001`/`customer_ids`/`result=map`/`spend_365d_total` 扫描 0 命中；同一次运行的本地报告仍保留完整 result 细节，确认修复只收紧了 sink 一侧。
- `test`：新增 `eval/langfuse` 的 `TestUploadReportNeverForwardsEvidenceValues`，用包含 `customer_ids` 的 `Reason` 断言上传体不含任何 evidence 值或敏感标记，并断言 metadata 恰好是封闭键集、失败 Score 仍保留 Boolean 判定与结构化 comment。
- `note`：验证用的临时配置与数据集只写在 `tmp/`（已被 gitignore），凭证仅通过环境变量传入，未写入仓库任何文件；验证结束后临时凭证文件已删除。

## Langfuse 项目隔离（2026-10-01）

- `fact`：本机自托管 Langfuse 3 由**父工作区**部署（`/home/huang/workspace/suanming-agent/deploy/langfuse/docker-compose.yml`），组织 `suanming-local`，原先只有 `agent-runtime` 一个 project。该 project 里 247 条 scores 含父项目自己的评测历史（`turn_type_match`、`route_primary_match`、`task_intent_match`、`sse_done`、`answer_factuality_pass`、`answer_scope_safe`、`observation_knowledge_search_present`，时间 2026-07-10 与 07-31）。此前本模板的 `-langfuse-upload` 一直写进这个共享 project。
- `implemented`：在同一实例的 `suanming-local` 组织下新建独立 project `eino-supervisor-template`（id `cmup940wk0015qw07m1fgepqv`）。建项目走应用自身的 tRPC（`projects.create`，需要 org OWNER 会话）；创建项目 API key 走 `projectApiKeys.create`。**未**直接改数据库或伪造 key 哈希。
- `verified`：新 key 经 `/api/public/projects` 确认只绑定 `eino-supervisor-template`；运行时该项目的 scores/traces 从 0 变为 4 scores + 1 trace（`code_version=project-isolation`），同时 `agent-runtime` 的 `totalItems` 运行前后均为 247，证明隔离生效、未再污染父项目。
- `implemented`：本机凭证写入仓库根目录被 gitignore 的 `.env`（权限 0600），字段为 `LANGFUSE_API_URL`、`LANGFUSE_PUBLIC_KEY`、`LANGFUSE_SECRET_KEY`、`LANGFUSE_OTLP_HEADERS`。`cmd/server` 自动加载 `.env`，`cmd/eval` 需要先 `set -a; . ./.env; set +a`。
- `note`：`LANGFUSE_OTLP_HEADERS` 的值含空格，在 `.env` 中必须加引号，否则被 shell 截断为 `Authorization=Basic`，OTLP 导出返回 `401 Invalid authorization header`，该错误会进入 evidence 并让用例判失败（已实测复现并修正）。
- `corrected`：清理本会话早期验证写入 `agent-runtime` 的 scores 时，`DELETE /api/public/scores/{id}` 返回 202 后列表短时间内不变，我据此判断"删除未生效、需 ClickHouse mutation"——该判断错误：删除是**异步**的，稍后复查 `leak-*` 残留为 0，`agent-runtime` 由 250 降为 247。未对共享数据库做任何 mutation。
- `verified`：`agent-runtime` 全量 247 条 scores 扫描 `cust-00`/`customer_ids`/`result=map`/`spend_365d_total`，敏感命中 0。

## L0/L1 门禁补强与改坏验证（2026-10-01）

- `implemented`：CI 从 4 步补到两层门禁。`.github/workflows/ci.yml` 现在依次执行 `gofmt -l cmd internal eval`（有输出即失败）、`go vet ./...`、`go run ./cmd/archcheck`、`go test ./...`、`go test -race ./...`、`go test ./eval/...` 和验收用例 `cmd/eval`；此前 CI 只有 archcheck + test + eval，**缺格式检查、vet 和 race**，比仓库自己文档要求的提交前门禁还弱。
- `implemented`：验收报告作为 artifact 上传（`eval-report-ci.json` + 新增的 `eval-report-ci.jsonl`），`if: always()`，即使门禁失败也上传——失败时才是最需要证据的时候；保留 30 天。
- `implemented`：新增 `eval/mutation-gate.sh`：对受保护行为注入真实故障（改源码 → 跑 L0/L1 → 断言必须变红 → 逐字节还原，不使用 `git checkout` 以免影响未提交改动）。接入 CI 独立 job `mutation`，与主门禁并行。
- `defect`（已修复）：首次运行 6 条注入有 3 条未被抓到。逐条查证后分成两类：`cancel-signal-lost` 与 `idempotency-dedupe-off` 是**我注入点选错**（改到了与故障无关的代码路径：取消的真正机制是 `control.cancel()` 的 per-run signal，幂等的真正机制是 fake 工具的 `outreachByKey` 去重表）；`policy-allowlist` 是**真实覆盖空缺**——`policy_test.go` 只覆盖"未注册 Tool"和"模型越权升级"，没有任何测试覆盖"已注册但不在 Worker allow-list 内"的 Tool，验收集也不覆盖（fake supervisor 只挑合法工具），因此关掉这条检查全绿。
- `fix`：新增 `TestPolicyGateRejectsToolOutsideWorkerAllowList`，断言已注册但越出白名单的 Tool 被拒绝且错误信息指明 allow-list。
- `verified`：最终矩阵 6/6 全被抓到。`policy-allowlist`/`approval-bypass`/`final-guard-off`/`terminal-uniqueness-off` 为 L0 命中（前三者 L1 也命中）；`cancel-signal-lost` 与 `idempotency-dedupe-off` **仅 L0 命中**——即验收数据集本身抓不住这两类回归，目前靠 Go 合同测试兜底，这是后续要补的覆盖缺口。
- `verified`：注入后源码逐字节还原（5 个文件 md5 一致，无 `false &&` / `return nil` 残留）。
- `note`：`runtime-real`、`coding-*`、Langfuse 版本与 v4 收口状态见上一节；本节只涉及本地确定性门禁，不依赖网络、模型或 Langfuse。

## 003 评测加固实施进展（2026-10-01）

- `implemented`：报告新增 `report_schema_version`（当前 `2`），JSON 与 JSONL envelope 同时携带且相等；消费方（基线比较）据此拒绝不认识的报告，而不是按新语义误读。
- `implemented`：fixture 由硬编码改为**启动期注册表**。`eval/runner/fixtures.go` 定义 `Fixture`（`Name`/`Configure`/`Cleanup`）与 `FixtureRegistry`（重复名与非法名在注册时失败）；`eval/loader.go` 新增 `LoadRegistered`，未注册 fixture 返回 `evaluation_setup_error`；`eval/runner` 与 `cmd/eval` 均改用注册表。契约放在 `eval/runner` 而非 `eval`，因为调整环境需要触碰 `internal/config`，而 `eval` 保持无基础设施依赖。
- `verified`：未注册 fixture 实测退出码 `2` 且**不写报告**；两个随仓库发布的 Dataset 仍为 `8 passed / 0 failed` 与 `1 passed / 0 failed`。
- `verified`：`TestRunCaseRegistryIndirectionKeepsVerdictStable` 证明经注册表运行与默认路径的 `verdict_digest`、终态、fake write count、cleanup 结果完全一致——注册表是间接层，不是行为变更。cleanup 失败与 configure 失败都记入证据并使用例失败。
- `verified`：改坏验证在 fixture 改造后仍为 **6/6 全部抓到**。
- `implemented`：基线回归门禁。`eval/baseline.go` 提供版本化快照（`LoadBaseline`/`WriteBaseline`/`Compare`），`cmd/eval` 新增 `-baseline` 与 `-write-baseline`。比较结论固定为 `regression`/`missing`/`new`/`still_failing`/`unchanged`。
- `verified`：门禁语义实测——与基线一致退出码 `0`；让一条基线中通过的用例失败得到 `regressions=1` 并打印 `positive-audience-query regression`，退出码 `1`；dataset / evaluator 版本不一致均为 `evaluation_setup_error` 且退出码 `2`。兼容性检查在运行任何 Case 之前完成，因此不可比的运行**不留下报告**。
- `verified`：`-write-baseline` 拒绝从有失败的运行生成基线（退出码 `2`），避免把坏状态批准成基线。
- `deferred`：US1（报告发布到 Langfuse）、US2（L1 独立抓住取消/幂等/白名单越权三类回归）、US3（第二个 fixture 证明扩展路径）、US5（judge 校准记录格式）尚未实施；对应任务在 `specs/003-evaluation-hardening/tasks.md` 中仍未勾选。

## 003 范围收窄与回退（2026-10-01）

- `reverted`：**放弃 fixture 启动期注册表**。实施中发现该抽象在生产代码里只会有一个实现，且 `Configure`/`Cleanup` 都是空实现——典型的"为单一实现造框架"，违反宪法的最小实现原则。已删除 `eval/runner/fixtures.go` 与 `fixtures_test.go`，并把 `eval/loader.go`、`eval/runner/runner.go`、`cmd/eval/main.go` 回退到最小改动：只校验 `preconditions.fixture` 的名字格式（`[a-z][a-z0-9_]*`），实际被测环境由 `-config` 决定。回退后生产代码中注册表痕迹为 0。
- `corrected`：plan 与 tasks 里"US2 依赖 fixture 注册表"是**未核实的假设**。执行中取消与白名单越权所需的场景，都可以用新配置或新消息驱动实现，不需要注册表。该依赖已撤销。
- `rescoped`：本功能收窄为纯增量、非侵入的三项——`report_schema_version`、fixture 引用格式的最小校验、基线回归门禁。原 US1（发布到 Langfuse）、US2（L1 独立覆盖）、US3（扩展路径）移出为独立功能；原 US5（judge 校准格式）**删除**，因为 judge 本体不存在，为其定义格式属于为假设中的未来需求增加抽象。
- `verified`：回退与收窄后实测——两个随仓库 Dataset 仍为 `8 passed / 0 failed` 与 `1 passed / 0 failed`；非法 fixture 名退出码 `2`；基线一致 `0`、退化 `1` 并指名用例、版本不一致 `2` 且不写报告；改坏验证仍 **6/6**；`gofmt`/`go vet`/`archcheck`/`go build`/`go test ./...`/`go test -race ./...`/`git diff --check` 全部通过。
- `verified`：JSONL 报告为 9 行（1 条 envelope + 8 条用例），envelope 的 `report_schema_version` 为 `2`。

## 评测闭环缺口攻坚：执行中取消（2026-10-01）

- `implemented`：新增 `chat_cancel` 动作与 `cancel_after_ms` 字段，让验收用例能在 Run **仍在执行**时发出取消。此前唯一的取消用例是在**等待审批**时取消，而 `registerRun/unregisterRun` 只包住 worker 执行窗口（`service.go:650→685`），所以那条路径根本走不到取消信号，改坏验证里 `cancel-signal-lost` 只有 L0 变红。
- `implemented`：`tools.*.fake_delay_ms` 配置让 fake Tool 可控地慢下来，并**响应取消**（等待期间 select ctx.Done）。只影响 fake 实现，真实实现忽略。`config.example.toml` 的 `user_query` 设为 500ms。
- `verified`：新增用例 `boundary-cancel-while-executing`，实测 `terminal=canceled`、`elapsed_ms=153`、事件序列 `started/decision/plan/progress/tool_call/canceled`；全量数据集 **9 passed / 0 failed**。
- `verified`：改坏验证矩阵中 `cancel-signal-lost` 由 **L1 green 变为 L1 RED** —— 「执行中取消」这类回归现在由验收数据集自己抓住，不再只靠单元测试。
- `finding`：**HTTP 层不是实时流**。`handler.go` 的 `writeEvents(c, service.Events(runID))` 在 Run 结束后才一次性写出事件，`Flush()` 只是形式；实测客户端读到 `progress status=running` 的时刻是 +1503ms（工具已跑完）。影响：客户端无法观察执行中进度，也不能用事件驱动取消，只能依赖确定性的时间窗口。这不属于本轮修复范围，但会影响任何期望"实时 SSE"的调用方。
- `finding`：另外两个目标经查证在 HTTP 层**结构性不可达**，不是漏写用例：幂等二次执行要求 `Status=Running && AttemptStatus=="prepared"`（`service.go:885`），即 prepared→running 之间进程中断，单进程内无法制造；白名单越权要求路由的 tool 不在 worker allow-list 内，而 `config.go:337` 在启动期就拒绝这种配置，fake supervisor 只会提出通过校验的路由。两者继续由 L0 合同测试覆盖。
- `implemented`：改坏验证增加**互斥锁**（`.mutation-gate.lock`）。该脚本会临时改坏源码，与其他构建/测试并发时对方会编译到被注入的代码——本会话我自己就踩过一次（并行跑 L0 门禁导致 `go vet`/`go test` 报语法错误），已加锁并在用法中写明必须独占运行。
- `implemented`：用例集由 8 增至 9，按既有纪律**升 dataset 版本到 2**（`eval/datasets/synthetic-operations-v2.json`），并重新生成 `eval/baselines/synthetic-operations-v2.json`；旧 v1 基线与数据集已删除，文档/CI 引用同步更新。
- `verified`：基线门禁在 v2 下复验——一致退出码 `0`（`unchanged=9`）；让一条基线通过的用例失败得到 `regressions=1` 且退出码 `1`；dataset 与 evaluator 版本不匹配均为 `evaluation_setup_error` 且退出码 `2`。

## 评测闭环收口（2026-10-01）

- `implemented`：**需求 ↔ 用例对照表**。`eval/coverage.json` 记录每条评审过的 claim 指向哪些用例，或显式豁免并写明理由；`eval/coverage.go` 实现校验，`cmd/eval -check-coverage` 只做引用完整性检查后退出。指向不存在的用例、既豁免又映射、既不映射也不豁免，都以退出码 `2` 失败；**没有 claim 指向的用例只提示不失败**。CI 在跑验收用例之前执行这一步。
- `implemented`：**失败归因**。`eval/evaluator/triage.go` 把每条失败断言变成可执行待办：用例、断言、失败类别、"先看哪里"、以及收尾要求（补一个能抓住它的用例并用改坏验证证明）。类别→怀疑对象的映射固定七条，未知类别标注"需人工判断"而不是丢弃。`cmd/eval -triage` 输出 JSON，同时把 markdown 打到终端，CI 作为 artifact 上传。
- `verified`：覆盖率校验实测 `claims=10 covered=8 waived=2 cases_without_claim=0`；退化运行实测输出归因表并指出 `positive-audience-query | result | business_expectation`。`TestShippedCoverageMatchesShippedDataset` 防止对照表与数据集悄悄漂移。
- `implemented`：两条**结构性不可达**的保护写入对照表豁免并附理由——Worker 白名单越权（启动期配置校验已拒绝违规路由）、幂等重放（需要 prepared→running 之间的进程中断）。它们继续由 L0 合同测试覆盖，不再伪装成 L1 覆盖。
- `implemented`：重新生成未变化的基线时沿用原批准时间（`Baseline.SameVerdicts`），基线 diff 从此只代表判定或版本真的变了。
- `implemented`：改坏验证增加工作区互斥锁与残留自检；`eval/check-residual.py` 用完整注入文本比对（首行比对对"以原文为前缀"的注入会假阳性）。
- `implemented`：重写 `.agents/skills/project-feature-evaluation/SKILL.md`：分层门禁与阻断权、三条纪律（证据缺失即失败 / 未运行不得标记通过 / 报"没抓到"前先确认注入有效）、闭环三要素、版本纪律、明确不做什么（judge 进门禁、单一实现的抽象、为不存在的东西定格式、通用 DSL、覆盖率指标）、以及状态词表（含"结构性不可达"）。
- `deferred`：CI 两个 job 需在仓库设置里设为 Required Check 才真正阻断合并；这是平台操作，不是代码。报告发布到 Langfuse 仍未实施。

## 003 收敛闭环：基线接线与门禁一致性（2026-10-02）

- `implemented`：基线快照记录逐用例 `verdict_digest`。该字段此前只声明从未写入，快照里只有 pass/fail 标签；现在由报告 `evidence.verdict_digest` 投影写入，`SameVerdicts` 把 digest 计入"测量身份"，`Compare` 仍只看 pass/fail（digest 变化既不会把绿灯判红，也不会把红灯判绿）。
- `verified`：用一次真实通过的运行重新生成 `eval/baselines/synthetic-operations-v2.json`——9 条用例的 pass/fail 与旧快照逐条一致，只是新增 digest 字段与一次批准时间；紧接着再生成一次**逐字节无差异**，说明 digest 跨独立运行稳定，T026 的"无意义 diff 不出现"在新格式下继续成立。
- `implemented`：CI 新增独立步骤 `baseline regression gate`，在**完整数据集**（`-risk high`）上跑 `-baseline`，报告一并作为 artifact 上传。此前基线能力只被本地手工命令使用，仓库门禁从不比较已批准基线，基线脱节是静默的。
- `verified`：该步骤命令实测退出码 `0`（`unchanged=9`）；窄选集叠加基线会因 `missing` 误报（`-risk low` → `missing=6`、退出码 `1`，与回归无关），因此 CI 固定全量，该交互已写入 `docs/evaluation-method.md` 与 `README.md`。
- `verified`：退出码契约现在可由文档配方复现——`go run ./cmd/eval` 把任何非 0 退出码折叠成 `1`，构建后的二进制才返回 `1`（退化，输出 `positive-audience-query regression`）与 `2`（版本不一致、不写报告）。`quickstart.md`、`README.md`、`docs/evaluation-method.md` 中**断言退出码**的配方已改为 `go build -o ./tmp/eval ./cmd/eval` 后的二进制并写明原因。
- `verified`：改坏验证在改动后的树上复跑，矩阵为 `policy-allowlist`（仅 L0）、`approval-bypass`/`final-guard-off`/`terminal-uniqueness-off`/`cancel-signal-lost`（L0+L1）、`idempotency-dedupe-off`（仅 L0），**6/6 caught**，退出码 `0`。
- `corrected`：`quickstart.md` §2 原先要求三类注入都由 L1 抓住，与已记录的豁免相矛盾。现按实测矩阵写明：取消已由 L1 独立抓住（`boundary-cancel-while-executing`），幂等重放与白名单越权在 L1 结构性不可达（豁免与代码证据在 `eval/coverage.json`），并指向 `spec.md` 的范围调整。同时修正 `docs/evaluation-method.md` 的 Case 清单（8 → 9，补 `boundary-cancel-while-executing`）。
- `verified`：`gofmt -l cmd internal eval` 无输出，`go vet ./...`、`go run ./cmd/archcheck`、`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`git diff --check` 全部通过；quickstart §1/§3 配方实测退出码 `0`（`cases=9 passed=9 failed=0`、`unchanged=9`）。
