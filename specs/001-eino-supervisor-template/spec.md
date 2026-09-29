# Feature Specification: Eino Supervisor Template

**Feature Branch**: `001-eino-supervisor-template`

**Created**: 2026-09-20

**Status**: Draft

**Input**: User description: "提供一个可独立运行的运营 Agent 基础模板，保留有界多 Agent
执行、人工审批和可恢复执行，不包含真实运营业务。"

## Clarifications

### Session 2026-09-24

- Q: 单 Worker 和多 Worker 示例应该通过哪种入口触发？ → A: 共用现有 `POST /api/chat`，通过用户输入触发，不新增示例专用 HTTP 接口。

### Session 2026-09-26

- Q: 这次是否要把“完整可观测性体系”纳入当前 Feature Spec 的必做交付范围？ → A: 纳入当前 Spec，但作为独立的基础设施阶段，先于业务案例验收。
- Q: 可观测性体系是否需要支持多实例生产部署下的统一日志、Trace 和 Metric 查询？ → A: 仅支持本地单实例，内存/文件观测用于开发和合同测试。
- Q: 本地单实例观测数据是否需要持久化到文件，并接入 Langfuse？ → A: 日志和 Trace 持久化到本地文件；同时按 Langfuse 的 OTLP 接入标准导出，默认合同测试不依赖外部凭证。
- Q: 这个 MySQL 案例应通过现有 `POST /api/chat` 触发为已注册的 Worker/Tool，还是新增专用数据库 HTTP 接口？ → A: 复用现有 `POST /api/chat`，注册 MySQL 查询和插入能力；插入需要审批。
- Q: MySQL 案例应围绕哪种数据表设计查询和插入操作？ → A: 使用用户、商品和简易订单等业务表。
- Q: 简易订单是否需要独立的订单明细表，以支持一个订单包含多个商品？ → A: 不需要；使用 `users`、`products`、`orders` 三张表，订单直接关联一个用户和一个商品。
- Q: GORM 的 OpenTelemetry 接入应如何验收？ → A: 自动化测试验证 GORM 查询/插入 span；Docker Compose 可选启动 OTLP Collector 查看真实 trace。

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 执行只读运营查询 (Priority: P1)

运营人员提出一个只读分析请求时，系统识别请求对应的已注册能力，执行受限分析并持续返回可理解的进度和最终结果。

**Why this priority**: 这是模板最小可用价值，后续业务能力都依赖同一条请求、决策、执行和结果返回链路。

**Independent Test**: 提交一个示例用户分析请求，验证系统返回一个最终结果和唯一完成事件，且只调用声明为只读的能力。

本模板先验收一个通过现有 `/api/chat` 触发的单 Worker 示例；该示例完成后，再验收同一入口触发的有界多 Worker 示例。

**Acceptance Scenarios**:

1. **Given** 已注册只读分析能力，**When** 用户提交匹配请求，**Then** 系统完成分析并返回结构化结果摘要。
2. **Given** 用户请求不匹配任何已注册能力，**When** 系统处理请求，**Then** 系统拒绝猜测执行目标并返回澄清或拒绝结果。
3. **Given** 分析能力返回不可恢复错误，**When** 系统处理请求，**Then** 系统返回分类错误且不产生写入动作。

---

### User Story 2 - 审批副作用操作 (Priority: P2)

运营人员请求一个具有副作用的示例操作时，系统展示待审批动作；只有明确批准后才执行一次，并能返回最终结果。

**Why this priority**: 运营 Agent 的关键风险在外部写入，模板必须展示可拒绝、可审计、不会重复执行的最小路径。

**Independent Test**: 提交一个模拟触达请求，验证未批准时不执行；批准一次后只产生一次模拟写入。

**Acceptance Scenarios**:

1. **Given** 请求需要副作用能力，**When** 系统生成执行计划，**Then** 系统进入等待审批状态且不执行该动作。
2. **Given** 存在等待审批的动作，**When** 审批被拒绝，**Then** 系统结束执行且不调用副作用能力。
3. **Given** 存在等待审批的动作，**When** 审批被批准并重复提交恢复请求，**Then** 系统最多执行一次并返回同一执行结果。

---

### User Story 3 - 从中断处恢复 (Priority: P3)

运营人员在执行被中断后，可以根据同一执行标识恢复等待中的工作，而不会重新执行已完成步骤。

**Why this priority**: 审批和长运行任务需要可恢复性，但它建立在 P1 和 P2 的基础上。

**Independent Test**: 在一个步骤完成后中断执行，恢复后验证已完成步骤未再次运行，剩余步骤继续完成。

**Acceptance Scenarios**:

1. **Given** 有可恢复的未完成执行，**When** 系统收到恢复请求，**Then** 系统从下一个未完成步骤继续。
2. **Given** 已经终态的执行，**When** 系统收到恢复请求，**Then** 系统返回原终态而不重新执行。

### Edge Cases

- 模型返回无法解析或指向未注册能力时，系统拒绝执行并保留错误分类。
- 外部能力在调用尚未开始且返回可分类失败时，系统才按声明的重试策略重试；调用已开始但结果提交未知时，系统返回 `RUN_OUTCOME_UNKNOWN`，进入人工核对等待，不自动重试，也不会重新运行已完成的副作用步骤。
- 客户端在事件流中断开时，系统保留可恢复状态，公开事件不泄漏内部路径、凭证或未授权上下文。

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: 系统 MUST 为每次请求生成可追踪的执行标识，并返回该标识关联的终态或等待状态。
- **FR-002**: 系统 MUST 仅执行已注册且通过策略检查的能力。
- **FR-003**: 系统 MUST 为每个执行设置步骤、时间和成本或调用次数上限。
- **FR-004**: 系统 MUST 区分只读能力与副作用能力，并在副作用能力执行前要求明确审批。
- **FR-005**: 系统 MUST 为副作用能力提供幂等保护，使重复恢复或重试不产生重复结果。
- **FR-006**: 系统 MUST 将执行进度和终态以有序事件提供给客户端，且每次执行只发送一个终态事件。
- **FR-007**: 系统 MUST 保存恢复所需的最小执行状态，并能区分完成、失败、等待审批和可恢复状态。
- **FR-008**: 系统 MUST 对模型决策、能力输入和能力输出执行结构化校验；校验失败时不得猜测替代目标。
- **FR-009**: 系统 MUST 提供一个不访问真实运营系统的只读示例能力和一个需要审批的模拟副作用能力。
- **FR-010**: 系统 MUST 记录每次执行的决策、计划、能力调用、审批结果、错误分类和终态，供后续排查。
- **FR-011**: 除健康检查外，系统 MUST 要求 API 请求携带可验证的认证凭证，并将其映射为不可由客户端 JSON 覆盖的主体。
- **FR-012**: 系统 MUST 在 run 创建时绑定主体，且在重放、审批、恢复和取消前校验资源所有权；该资源授权 MUST 与模型动作的 Policy Gate 分离。

### Runtime Extension Requirements (M5-M8)

以下要求扩展同一组 P1-P3 用户故事的运行内核，不改变现有 HTTP/SSE 入口或权限边界：

实现约束：先检查仓库已有依赖与本地参考源码中可独立复用的成熟能力；优先复用标准库和已有库，也允许直接移植或模仿 Coze 的文件/小模块。移植实现必须通过本项目合同测试并接回现有边界；不把许可证核验设为这个个人开源脚手架的实现门槛。不得整套搬入 Coze 平台子系统、商业多租户或绕过 Policy Gate、审批、幂等和审计；只有可复现测试证明现有能力无法满足需求时，才新增自研实现。

- **FR-013**: 系统 MUST 使用统一错误分类和重试策略；只有外部调用尚未开始的已知 pre-call failure 可重试，in-flight/commit 未确认必须进入 `waiting_reconciliation`，不得自动重试。
- **FR-014**: 系统 MUST 通过进程内有界 Tool Pool 管理已注册 Tool 的并发、deadline、租约和生命周期；Tool Pool 不得改变 Policy Gate、审批或终态决定。
- **FR-015**: 系统 MUST 提供按 `tenant_id + subject_id + conversation_id` 隔离的短期会话 Memory，执行大小、TTL、敏感字段和召回上限约束；本特性不实现跨会话长期事实记忆、向量检索或 Memory 自动摘要。
- **FR-016**: 系统 MUST 由 Application/Manager 持有多步骤 `ExecutionPlan` 的推进、预算、checkpoint 和终态；`WorkerRunner` 每次只执行一个已批准的有限步骤并返回结果，不得新增步骤、Tool、审批或终态事件。
- **FR-017**: fake/function Runner 与 Eino Runner adapter MUST 通过同一 `WorkerRunner` 合同测试，且不得把 Eino 类型带入 domain/application。
- **FR-018**: 系统 MUST 通过现有 `POST /api/chat` 触发单 Worker、多 Worker 和 MySQL 数据库案例，不新增示例专用 HTTP 接口；单 Worker 示例 MUST 先于多 Worker 示例及数据库案例完成独立验收。
- **FR-019**: MySQL 数据库案例 MUST 使用用户、商品和简易订单业务表，提供至少一个真实查询操作和一个受审批保护的订单插入操作。
- **FR-020**: MySQL 案例 MUST 使用三表关系：`orders.user_id` 关联 `users.id`，`orders.product_id` 关联 `products.id`；订单至少保存数量和金额，不要求订单明细表。
- **FR-021**: MySQL 案例 MUST 为查询和插入操作接入 GORM OpenTelemetry 追踪；自动化测试 MUST 能验证对应数据库 span，OTLP Collector 仅作为本地可选观测依赖。
- **FR-022**: 系统 MUST 提供完整的运行时可观测性基础设施阶段，且该阶段 MUST 在 MySQL 业务案例验收前完成独立验收。
- **FR-023**: 系统 MUST 输出结构化日志，至少关联 `service`、`env`、`level`、`trace_id`、`span_id`、`request_id`、`run_id`、`worker_id`、`tool_id`、`phase`、`error_code`、`attempt` 和 `duration_ms`；日志不得包含原始用户消息、Prompt、凭证、Authorization、完整 Tool 输入输出或内部文件路径。
- **FR-024**: 系统 MUST 使用标准 OpenTelemetry 上下文贯通 HTTP 请求、Application Run、Supervisor/Model、Worker、Tool、Checkpoint、Event 和 MySQL 查询/插入 span；客户端提供的 W3C `traceparent` MUST 能被提取，公开响应 MUST 能返回可检索的 trace 标识。
- **FR-025**: 系统 MUST 提供低基数指标，至少覆盖 HTTP 请求量/延迟/错误、Run 终态、Model/Tool 调用量与延迟、重试、审批等待、恢复、预算耗尽和观测导出失败；`run_id`、用户输入和错误全文不得作为 Metric label。
- **FR-026**: 系统 MUST 统一错误诊断字段和公开错误边界；内部日志/Trace 至少保留 `error_code`、`error_class`、`phase`、`retry_decision` 和安全的错误 fingerprint，客户端只接收稳定分类、消息和允许公开的 run/trace 标识。
- **FR-027**: OTLP Collector 或日志后端不可用时，业务执行 MUST 继续按原有状态、审批、幂等和终态合同运行；系统 MUST 产生一次可检索的观测降级信号，且不得因观测失败改变业务结果。
- **FR-028**: 当前可观测性范围 MUST 限定为本地单实例开发与合同测试；不要求跨实例 Trace/Log/Metric 聚合、共享观测存储、观测数据高可用或生产级观测平台。
- **FR-029**: 系统 MUST 为本地单实例保留结构化日志和 Trace 文件快照，具备文件权限、大小/日期轮转和保留策略；Metric 不要求写入本地文件。
- **FR-030**: 系统 MUST 通过标准 OpenTelemetry OTLP/HTTP 接入 Langfuse，使用配置化 endpoint、service name 和环境变量 headers；API key、Authorization 和用户内容不得写入配置文件、日志、Trace 属性或事件正文。
- **FR-031**: Langfuse 导出 MUST 是可选观测依赖；未配置或不可用时，业务状态、审批、幂等、错误和终态合同 MUST 不受影响，并产生结构化降级信号。默认合同测试 MUST 使用本地 fake/collector stub，不要求真实 Langfuse 凭证。
- **FR-032**: 仓库 MUST 提供 Docker Compose 依赖入口，仅启动上游 MySQL 以及可选 observability profile 的 OTel Collector；Go 服务 MUST 通过宿主机 `go run` 启动并连接宿主机暴露的依赖。Langfuse 可以是外部 OTLP 目标，不要求将其存储平台一并部署。宿主机 `go test ./...` 和 `go run` 路径 MUST 保持可用且不依赖 Docker。
- **FR-033**: Run 的持久化快照 MUST 包含计划/步骤状态、attempt、终态、错误分类和更新时间；同一个 Run MUST 通过带 owner token 和到期时间的持久租约串行化执行及状态写入，过期租约允许新 owner 接管。已持久化为 in-flight 的步骤恢复时 MUST 进入 reconciliation，不得推测 Tool 未执行并自动重试。
- **FR-034**: Plan、checkpoint 和 event store 未处于同一数据库事务时，系统 MUST 把部分失败保留为可恢复状态，并由 Resume 根据持久化 Plan 修复 checkpoint/event 投影；文档和接口 MUST 不宣称跨存储 exactly-once。副作用未知结果 MUST 继续走既有 reconciliation 合同。

### Key Entities *(include if feature involves data)*

- **执行请求**: 一次用户发起的运营任务，包含输入、执行标识和关联会话。
- **路由决策**: 候选能力、风险级别、所需参数和策略检查结果。
- **执行计划**: 有序执行步骤、每步状态、预算和恢复信息。
- **审批请求**: 待批准动作、审批状态、执行标识和可审计原因。
- **能力声明**: 某个 Worker 或 Tool 的输入输出、风险等级、权限和重试约束。
- **运行事件**: 执行过程和终态的内部记录，可投影为客户端事件。
- **MySQL 案例数据**: 用于本地验收的用户、商品和简易订单记录，不代表真实客户数据。
- **用户、商品和订单**: MySQL 案例中的三类持久化记录；一个订单关联一个用户和一个商品。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 在本地 fake 能力环境中，P1 只读流程从请求到唯一终态可在 5 秒内完成。
- **SC-002**: 在批准前，100% 的副作用测试请求不产生模拟写入记录。
- **SC-003**: 对同一批准执行重复恢复 10 次，模拟副作用能力的执行次数保持为 1 次。
- **SC-004**: 在模型输出无效、能力未注册、审批拒绝和能力超时四类场景中，100% 的测试运行返回可分类终态且不触发未授权写入。
- **SC-005**: 中断后恢复的测试中，100% 的已完成步骤不被再次执行。
- **SC-006**: M5 错误合同测试中，100% 的 unknown outcome 不自动重试，且同一错误在运行状态、公开错误和重试决策中保持一致分类。
- **SC-007**: M6 并发合同测试中，100% 的超并发、未注册、越权和非法输入调用在外部 Tool 调用前被拒绝。
- **SC-008**: M7 Memory 合同测试中，100% 的跨主体、跨租户、过期、超限和敏感记录不可被召回。
- **SC-009**: M8 Runner 合同测试中，fake/function Runner 与 Eino adapter 对同一已批准步骤计划通过相同的成功、失败、取消、恢复和 unknown outcome 断言。
- **SC-010**: 示例验收 MUST 先通过单 Worker `/api/chat` 流程，再通过同一入口的多 Worker 顺序流程；两类流程均不得新增未注册 Worker、Tool 或终态事件。
- **SC-011**: 在不发送真实用户消息、Prompt、凭证或 Tool payload 的前提下，HTTP `/api/chat`、审批、resume、cancel、Worker、Tool、Checkpoint、Event 和 MySQL 查询/插入测试 MUST 能通过同一 `trace_id` 关联日志和 Trace；带入合法 `traceparent` 时父子关系 MUST 保持。
- **SC-012**: 可观测性合同测试 MUST 覆盖结构化日志字段、敏感信息脱敏、HTTP/Run/Tool/MySQL span、低基数指标、错误分类和 Collector 不可用降级；观测后端故障时业务结果与无观测后端时一致。
- **SC-013**: 可观测性阶段 MUST 先于 MySQL 业务阶段通过；阶段完成后才允许将 MySQL 查询/插入标记为集成完成。
- **SC-014**: 在本地单实例、无外部观测后端的默认测试环境中，业务结果 MUST 与启用本地 OTLP/文件观测时一致；跨实例聚合和高可用观测不作为本特性的验收条件。
- **SC-015**: 文件观测测试 MUST 验证日志/Trace 可写、敏感字段脱敏、轮转/保留边界和进程重启后的可读取快照；Langfuse 集成测试 MUST 使用 OTLP/HTTP stub 验证 headers、service/resource 属性和 Trace/LLM/Tool span 到达，不使用真实凭证。
- **SC-016**: 提供 MySQL 密码环境变量时 `docker compose config` MUST 校验成功；Compose 启动的 MySQL MUST 达到健康状态，OTel Collector MUST 可通过 observability profile 单独启停，Go 服务由宿主机启动；默认 Go 单测不得要求 Docker daemon。
- **SC-017**: 租约合同测试 MUST 证明两个执行者竞争同一 Run 时仅一个 claim 成功、过期后可接管；恢复测试 MUST 证明重复 Resume 不重放已完成副作用、in-flight unknown outcome 不自动重试、terminal event 唯一且序号连续，并覆盖 Plan/checkpoint/event 部分失败后的可恢复路径。

## Assumptions

- 第一版面向开发者和运营 Agent 构建者，不提供终端用户运营工作台。
- 默认路径只使用本地 fake 能力和内存状态，不连接真实客户数据或外部营销渠道；文件/MCP 适配是显式可选扩展。
- 默认只支持静态 Bearer 主体、单一审批人和单一示例运营能力；完整 OIDC/JWT、RBAC、组织层级、多审批人和并行执行由后续规格定义。
- 单 Worker 与多 Worker 示例均使用现有 `/api/chat` 入口；多 Worker 示例限定为已注册、有限步骤的顺序执行，不在本特性中增加通用 DAG 或新的示例接口。
- 本特性只实现短期会话 Memory；跨会话长期事实、自动摘要、向量检索和外部记忆服务由后续独立规格定义。
- 客户端使用事件流接收进度；其他接入方式可在不改变运行合同的前提下后续增加。
- 可观测性只面向本地单实例开发、调试和合同测试；OTLP Collector 可以作为本地可选依赖，不构成本特性的生产部署承诺。

### Synthetic Operations Contract

合成运营案例使用固定快照 `as_of=2026-09-25`，fixture 只包含匿名
`customer_id`、`last_order_at`、`spend_365d`（整数元）、`last_touch_at` 和
`contact_consent`。固定八行数据和结果由 `internal/infrastructure/examplebusiness/fixture.go`
及其合同测试维护；匹配结果必须按 `customer_id` 字典序返回
`cust-001`、`cust-002`、`cust-006`、`cust-008`，`count=4`，
`spend_365d_total=6200`。

只读查询只接受严格 JSON `{"as_of":"2026-09-25"}`，不接受额外字段、尾随数据、自然语言
条件或其他日期。筛选条件是：`last_order_at` 严格早于快照日前 30 天、`spend_365d >= 1000`、
`last_touch_at` 缺失或不晚于快照日前 7 天、且 `contact_consent=true`；恰好 30 天不匹配，
恰好 7 天匹配，缺失触达时间按“从未触达”处理。零匹配返回
`{"count":0,"customer_ids":[],"spend_365d_total":0}`；缺失/非法 `as_of`、额外字段和不支持的
筛选条件返回稳定输入错误，不当作零匹配。

第一步结果只允许 `count`、`customer_ids`、`spend_365d_total`，最多 100 个匿名 ID，Tool 输出最多
1 MiB。第二步只允许返回 `count`、`spend_365d_total` 和有序 `segments`；segment 只能是
`dormant`、`consented` 或零匹配时的 `empty`，不得推断身份、法律同意或外部投放。模拟触达是
独立 run，只接收上述匿名结果或其 `customer_ids` 投影；必须经过既有审批和幂等键，拒绝姓名、电话、
邮箱、凭证及其他字段，不连接真实 CRM 或发送消息。
- Langfuse 作为可选外部观测出口；本地文件是默认可回放证据，真实 Langfuse 环境验证属于配置后集成测试，不作为零凭证合同测试前置条件。
