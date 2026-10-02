# 当前实现盘点与成熟项目对比

**盘点日期：** 2026-09-29
**范围：** 当前工作树、项目进度与设计文档，以及本地 Eino 模块缓存、Coze Studio 和电商 Agent 参考源码。
**状态标签：** `implemented` 有本地代码和验证证据；`partial` 有部分实现但闭环或生产语义未证实；`deferred` 明确暂缓；`not_applicable` 不属于本模板目标。

## 结论

这是一个以权限边界、运行状态、可恢复合同和本地确定性评测为核心的 Go/Eino Agent 脚手架，不是一个成熟的 Agent 平台。它已经完成了“请求进入、候选路由、确定性授权、有限计划、工具执行、审批/取消/恢复、事件投影、Case Runner、四维报告”的本地闭环；成熟项目领先之处主要不在分层，而在产品面、执行器丰富度、生产存储/身份、工作流编辑与发布、长期知识/记忆、运维和在线评测承载。

下一步优先不是添加 DAG、长期记忆或更多 Agent 抽象，而是让已完成的本地评测、架构检查、失败回流和边界状态具备可复核入口；随后再根据真实部署目标选择身份和持久化方案。`business_correctness` 已对合成 Case 的 `expected_results.result` 做字段级安全断言，但真实业务质量基准仍是 partial。

## 证据边界

- 本地事实以 `PROGRESS.md`、当前代码和合同测试为准；计划状态记录在本地 Spec Kit 工作区 `specs/`（随 `.specify/` 一起 gitignore，不随模板发布），其中的 plan/tasks 只是输入，不能当作功能已闭环。计划文档、配置字段或接口存在本身不等于功能闭环。
- 新鲜验证：`go test ./...`、`go test -race ./...`、`go build ./cmd/server/`、`go vet ./...`、`go run ./cmd/archcheck`、`git diff --check`，以及高风险 8 Case、低风险标签选择 3 Case 均通过；这些是当前工作树的本地证据。
- `examplebusiness/fixture.go` 已接入 `user_query`、`user_summary_query` 和模拟触达的本地 fake 链路，说明 Dataset 可从 HTTP/SSE 入口跑通；这不等于真实 CRM/营销业务或真实业务质量基准。
- 本轮没有联网调研。Temporal Go SDK 和 LangGraph 上游源码在当前工作区不可用，因此只记录为概念基线并标记 `deferred`，不把它们的实现语义当作本项目证据。

## 当前能力清单

| 领域 | 目前有什么 | 状态 | 主要限制 / 证据落点 |
| --- | --- | --- | --- |
| Agent 运行链 | HTTP/SSE → Application Run Manager → Supervisor → Policy Gate → 有界 ExecutionPlan → WorkerRunner → Tool → Final Guard → RunEvent | `implemented` | `internal/application/run/service.go`、`docs/architecture.md`；状态 owner 在应用/领域，不在 Agent 框架 |
| Supervisor | 确定性 fake 路由；可选 Eino DeepSeek 适配；模型输出严格 JSON 解析，未知路由失败关闭 | `implemented` | `internal/infrastructure/llm/`；真实 DeepSeek Supervisor 的只读 `/api/chat` 有历史 smoke 证据 |
| Worker/执行器 | 默认 SingleToolRunner；可选 Eino ADK Runner 和 ApprovedTool/ToolNode；启动时按 Worker 配置分派 | `partial` | 本地 deterministic ToolCallingChatModel 与兼容 HTTP stub 证明 ToolCall 闭环；真实模型驱动的 Eino Worker ToolCall 未验证 |
| 计划/多 Agent | 每个 run 最多两个有序步骤；逐步重新过 Policy Gate；执行预算限制 steps、calls、retry、cost、deadline | `implemented` | 当前是固定顺序的多 Worker，不是 ReAct、并行、DAG 或开放式自主循环；`internal/domain/agent/contract.go` |
| 审批与副作用 | side-effect 在批准前阻止执行；批准身份取认证主体；幂等键和审计事件；fake 模拟写入 | `implemented` | 没有真实运营写入；审批流程和业务数据闭环有限；`internal/application/run/`、`internal/infrastructure/tool/` |
| Tool | 注册表、Worker allow-list、Pool 并发/租约/deadline、输入输出合同校验、错误分类；fake、固定 endpoint HTTP GET、受控 MCP | `implemented` | MCP 只调用启动时 allow-list，不动态发现、不支持真实写入 Tool；HTTP Tool 是固定只读 GET |
| 认证/授权 | Bearer token SHA-256 环境变量验证；映射 tenant/subject；run 重放、审批、resume、cancel 做 owner 校验；和模型动作 Policy 分离 | `partial` | 静态凭证、静态主体；无 OIDC/JWT、RBAC/ABAC、组织/资源目录和 token 生命周期 |
| 状态/存储 | 内存与版本化文件 Repository、checkpoint、event store；Run lease 支持 owner token、expiry、claim/release；可选 MySQL 复用现有 GORM lease adapter | `partial` | 当前单实例目标由 memory/file 满足；MySQL GORM 用于示例订单 Tool 和可选 lease，不是完整 Agent Runtime 状态后端。真实 MySQL 多实例恢复未验证且当前不需要；Plan/Checkpoint/EventStore 仍是独立写边界，不支持跨存储事务或 exactly-once |
| 恢复/可靠性 | checkpoint 前置于 Runner；`prepared` 可回到 pending；过期接管把 in-flight `running` 转为 reconciliation；未知结果不重试；缺失投影可修复 | `implemented` | 本地 memory/file 合同和 Service 测试已覆盖；真实外部副作用、强杀进程和 Eino 跨进程恢复仍未验证 |
| Memory | 按 tenant/subject/conversation 隔离的短期 Memory，TTL/大小/敏感字段/召回限制，内存和文件实现 | `partial` | 不等于知识库或长期记忆；没有 embedding、向量检索、摘要、事实更新/遗忘策略 |
| 事件/API | `/healthz`、`/api/chat`、approval、resume、cancel；认证 Bearer；SSE 有序事件、唯一终态、公开数据脱敏 | `implemented` | 当前是小型服务 API，不是完整 SDK、控制面或运营工作台 |
| 配置/扩展 | TOML → 环境覆盖 → Validate；启动编译不可变 RuntimeCatalog；集中业务描述；注册缺失/allow-list 错误启动失败 | `implemented` | 配置只能选择已编译实现；没有热加载、动态插件或面向用户的 Agent Builder |
| 观测/评测 | 可选 OTLP HTTP trace/metric、Eino callback；本地 HTTP/SSE Runner、四维硬 Evaluator、强制 evidence matrix、事件完整性/Policy 顺序校验、Tool 风险元数据、JSON/JSONL 报告和失败回流合同 | `partial` | 本地 Case 与通用反绕过合同已实现；在线 Dataset/Score/Feedback 同步、Judge 门禁、告警/SLO/运营看板仍未实现 |
| 日志 | `log/slog` JSON 日志、低基数字段上下文、敏感关键字/内部路径脱敏和有界文件 writer | `partial` | 运行期诊断字段、指标覆盖和生产级日志平台仍有限；不把本地 logger 误称为完整运维体系 |
| Trace / Metric | OTLP HTTP exporter、HTTP trace context、Eino ChatModel/Tool callback、事件计数器、token usage、错误 span 状态 | `partial` | Case 关联和 context-aware EventStore 已有合同；生产 collector、完整延迟/错误/重试/审批/预算指标、多实例证据仍有限 |
| 错误处理 | 应用层统一错误分类、pre-call 重试、timeout/cancel、unknown outcome reconciliation；HTTP 稳定错误码和 SSE failed 事件 | `implemented` | `internal/application/run/error_policy.go`、`internal/application/run/service.go`、HTTP `statusFor`；仍缺统一错误日志字段、外部错误码/重试次数观测、panic/启动/关闭错误的结构化处理 |
| 示例业务 | 注册了分析、总结、模拟触达；固定合成客户 fixture 已接入本地 fake Tool 和评测 Case | `partial` | 没有真实运营系统或外部写入；字段断言只证明合成 fixture，不证明真实业务质量 |
| 部署/平台 | Go 服务入口、DeepSeek 默认样例配置、健康检查；本地 fake 可显式选择 | `partial` | 未提供完整生产部署拓扑、数据库/队列、密钥管理、水平扩展、备份迁移和发布回滚链路 |

## 架构差异

| 维度 | 本项目 | Eino | Coze Studio | Dify | LangGraph |
| --- | --- | --- | --- | --- | --- |
| 产品定位 | 安全合同优先的 Go 参考脚手架 | Go Agent/多 Agent SDK 和运行组件 | Agent 开发与发布平台 | LLM 应用开发平台 | Agent/Graph 编排与持久运行框架 |
| 编排能力 | 单步或最多两步固定顺序 | ADK 提供 ReAct、Supervisor、Sequential/Parallel/Loop、Plan-Execute、Runner | 可视化 Workflow，含节点、控制/数据流及 Agent/插件/知识资源 | 可视化 Workflow、Agent、RAG、模型管理 | 图状态/节点/边、checkpoint 与 interrupt/HITL |
| 状态与恢复 | 应用层 ExecutionPlan 为业务 owner；文件 checkpoint 是最小快照；unknown outcome 不自动重试 | Runner 管生命周期、checkpoint、interrupt/resume；能力取决于配置的 CheckPointStore | 平台管理 workflow/app/conversation/resource 生命周期 | 平台负责应用、Workflow 与部署服务 | thread checkpoint、恢复、time-travel、fault tolerance；Store 可跨 thread |
| 安全/权限 | 强制代码 Policy Gate、审批、幂等、资源 owner、Final Guard | 提供 Agent/Tool/Runner 原语，业务侧仍须设计权限和副作用治理 | 有平台级用户、资源、插件认证及多租户/发布边界 | 平台应用权限与插件/工具生态；具体部署版本能力应按 edition 核实 | 给出运行时中断/状态原语，不等于业务授权平台 |
| 扩展/产品体验 | 显式编译时 Catalog，无画布、插件市场或 Agent 管理界面 | 库级扩展接口，不是完整产品控制台 | Studio、画布、Agent/App 发布、插件、知识库、API/SDK | Web Studio、Workflow、Prompt IDE、模型供应商、RAG、API | 代码优先的图/运行框架；产品级 Agent Server 属额外方案 |
| 当前差距状态 | 基础受控链路 `implemented`；生产及产品能力多为 `partial/deferred` | Eino ADK 本地适配 `partial`；真实 Worker ToolCall/跨进程恢复未证实 | Workflow DAG、资源平台、多租户均 `deferred` | 产品功能面 `deferred`，不应为匹配其范围而扩张模板 | 采用基础 checkpoint 设计为 `partial`；pending writes/time travel/服务级运维未实现 |

### 关键判断

1. 本项目与 Eino 不在同一抽象层。Eino 是 Agent runtime/toolkit；本项目在它外面加了应用状态 owner、授权、审批、幂等和 SSE 合同。不要把“Eino 有 Supervisor”当作本地具备可靠业务审批或生产恢复的证据。
2. Coze/Dify 的领先是平台广度：画布、资源/应用管理、知识处理、模型供应商、插件/发布、API 产品化及运营能力。这些不是当前模板的缺陷，除非目标从“脚手架”改变为“平台”。
3. LangGraph 的恢复模型更丰富，特别是按 thread 的 checkpoint、HITL、time travel、失败恢复与 pending writes。本项目更窄且把业务计划与 checkpoint 分开；没有共享持久化和分布式调度之前，不应宣称相同恢复保证。
4. Temporal 是生产工作流基础设施参考，不是 Agent Builder 对标产品。其服务端持久执行、活动重试/心跳/worker 协调，与本地文件快照不是同级能力；只有部署和任务时长指标要求时才引入同类系统。
5. 当前最具体的能力短板不是“少一个框架”，而是业务结果字段评测、真实模型驱动的 Eino Worker ToolCall、跨进程恢复和生产身份/运维证据仍不完整。

## 基础工程能力专项

### 日志：已有最小结构化出口，生产诊断仍有限

当前使用 Go 标准库 `log/slog` 提供 JSON 日志、低基数字段上下文、敏感关键字/常见内部路径脱敏和有界文件 writer；`composition.App` 负责装配和关闭 writer，相关合同测试覆盖敏感字段与轮转边界。

仍未完成的是完整生产诊断合同：运行期每个阶段的统一 `run_id`/`trace_id`/`error_code`/`attempt` 字段、外部错误 fingerprint、完整延迟与审批/恢复指标、告警/SLO 和多实例日志采集。当前 logger 足以支持本地诊断和合同验证，不应被包装成生产日志平台。

### Trace / Metric：本地关联已接通，生产覆盖仍不完整

`internal/infrastructure/observability/observability.go` 已经能创建 OTLP HTTP trace/metric exporter，设置 service name、采样率、HTTP headers，并为 Eino ChatModel/Tool 建 span；Tool/模型错误会记录到 span，token usage 也会作为属性写入。事件写入还有 `run.event` span 和 `agent.run_events` counter。

当前实现已通过 HTTP trace context、SSE trace 关联和 context-aware EventStore 传递请求上下文；Case 的 `case_id`、`case_version`、`code_version` 和 evaluator version 作为低基数属性进入观测。Exporter、日志和 snapshot 故障会产生一次 degraded signal，业务状态保持不变。

仍有三个边界：

1. 指标覆盖还不足以支撑生产 SLO，尤其是完整 HTTP/Run/Tool/审批/恢复/预算延迟与错误维度。
2. 真实 collector 不可用、provider 多次初始化、多实例部署和跨进程关联证据仍有限。
3. Langfuse 的在线 Dataset/Score/Feedback 双向同步没有实现，本地 JSON 报告仍是默认评测事实来源。

### 错误处理：核心语义较完整，诊断出口不足

这一块是当前基础能力里最完整的部分。应用层已经区分：

- `pre_call_failure`：外部调用未开始，可以在预算内重试；
- `timeout` / `canceled`：分别映射超时和取消；
- `unknown_outcome`：调用后结果未确认，进入 `waiting_reconciliation`，禁止自动重试；
- `policy_denied`、`invalid_output`、`unavailable`、`business_failure`、`internal`：用于跨适配器归一化。

HTTP 层再把这些分类映射为稳定状态码和公开错误消息，SSE 使用 `failed` 事件，内部 cause 不直接返回客户端。这套“分类先于文本”的方向是正确的。

还需要补的不是再造一套错误框架，而是诊断和运维合同：

- 每个错误记录稳定 `error_code`、`error_class`、`phase`、`attempt`、`retry_decision`、`run_id`、`trace_id`；
- 外部错误保留安全的 provider/tool 分类和可检索 fingerprint，不记录完整响应或凭证；
- 为启动失败、panic、HTTP 500、shutdown/exporter 失败定义结构化日志和退出策略；
- 给重试、恢复、unknown outcome、Tool timeout 增加指标和告警条件；
- 统一错误包装，避免部分基础设施只返回自然语言 `errors.New(...)`，导致日志只能靠字符串解析。

### 基础工程优先级

如果按“先补基础再扩业务”排序：

1. **P0：保持本地评测闭环可复现**：固定 Dataset/Runner/Evaluator/报告命令，并在 `PROGRESS.md` 记录实际证据。
2. **P1：扩展业务结果字段断言**：当前已完成合成 fixture 的 `count`、`customer_ids`、`spend_365d_total` 安全解析；真实业务字段和质量基准仍需按业务合同补充。
3. **P1：补错误诊断字段和指标**：保留现有错误分类，只增加结构化日志、延迟/错误/重试/审批/恢复指标。
4. **P2：按真实部署需求补身份、存储和观测**：没有多实例或合规指标前，不提前引入新的平台组件。

不建议现在引入 Sentry、ELK、Prometheus 独立 SDK、复杂日志平台或自研 observability bus；OTLP 已经是现有出口，先把字段、上下文和生命周期补完整。

## 主流方案与可复用参考

### 主流基线是什么

成熟 Go 服务通常采用下面的组合，而不是某一个“标准日志库”：

```text
HTTP/RPC middleware
  -> request_id / trace_id / span_id / tenant / route
  -> structured logger
  -> OpenTelemetry traces + metrics
  -> OTLP / Prometheus / 日志平台
  -> error classification + alerting
```

常见实现有：zap、logrus、`log/slog` 做结构化日志；OpenTelemetry 做 Trace 和 Metric；OTLP、Prometheus、Jaeger/Tempo、Loki/ELK 或厂商平台负责存储与检索。真正的主流能力是字段和关联关系稳定，不是强制 zap 或 slog。

### 可以直接复用什么

| 参考 | 可直接借鉴 | 不应直接照搬 |
| --- | --- | --- |
| 当前父项目 Gin 服务 | `tracing.Middleware`、`StartTrace`/`StartSpan`、TraceID 注入响应、文件 trace 快照、OTel bridge、运行失败结构、debug trace 查询 | 自定义 Trace 类型不能替代 OTel 标准上下文；文件 trace 适合本地诊断，不等于生产 trace 存储 |
| go-zero `core/logx` | 日志配置项、JSON/plain 编码、级别、轮转/保留、字段、敏感值接口、`WithContext`、Trace 字段 | 不必搬整个 logx；当前项目先选一个日志库并保持应用层不依赖具体实现 |
| go-zero `core/trace` | OTLP exporter 选择、采样率、resource/service name、headers、非阻塞初始化、`sync.Once` 防止重复初始化、显式 StopAgent | 不要重复造第二套 tracer；统一使用当前 OTel SDK，并补标准 HTTP propagation |
| Coze Studio | OTel/Prometheus/Jaeger/zap/logrus 的生产组合、trace_id/span_id 贯穿、错误码体系、按服务/组件维度观测 | 不搬 Coze 的微服务观测平台、全量存储、商业资源和管理面 |
| 本仓库已有 observability | OTLP HTTP exporter、HTTP trace context、Eino callback、事件 span、token usage、脱敏和 degraded signal | 保持单一 OTel 出口，优先补指标和生产 collector 证据；不要增加另一个观测总线 |

### 对当前项目的直接判断

- `slog` 是**合理的最小日志实现**，但不是完整可观测性方案。
- go-zero 的 `logx + trace` 设计比当前入口更完整，尤其是日志配置、轮转、敏感值、context 和 exporter 生命周期；这些可以按文件/小模块模仿。
- 父项目的 `internal/tracing` 已经比当前模板多出“HTTP 中间件、业务 Trace 树、TraceID 返回、文件快照、运行错误投影”几层，适合优先移植接口边界和测试，不应原样引入自定义 Trace 与 OTel 并行的双轨复杂度。
- Coze 的方案证明成熟平台会同时使用日志、Trace、Metric 和错误码/运行快照；它不是“用了某个日志库所以成熟”。

### 推荐的落地顺序

1. 以当前 `observability.Runtime` 为唯一 OTel 出口，补 HTTP middleware、标准 W3C `traceparent` 提取/注入和 `context` 贯通。
2. 选择一个日志实现：本项目建议 `log/slog`；若要最大化复用现有父服务代码，则选 logrus。不要同时保留标准库 `log`、slog、logrus、zap 四套。
3. 定义最小字段合同：`service`、`env`、`level`、`timestamp`、`trace_id`、`span_id`、`request_id`、`run_id`、`worker_id`、`tool_id`、`phase`、`error_code`、`duration_ms`、`attempt`。
4. 增加四类指标：HTTP 请求、Agent Run、Tool/LLM 调用、恢复/审批/错误；高基数字段只能放 Trace/日志，不放 Metric label。
5. 增加一条真实 smoke：请求带 `traceparent`，日志、SSE `trace_id`、RunEvent span、模型/Tool span 能关联到同一条 Trace；Collector 不可用时业务仍可完成并产生降级日志。

## 后续计划

### P0：保持可复现的本地闭环

T035–T049 已在当前工作树完成并有本地证据：版本化 Dataset、HTTP/SSE Runner、四维确定性报告、失败回流合同、架构检查、CI 接线和可选观测关联。本地验证使用 `README.md` 和 `docs/evaluation-method.md` 中的固定命令，不把历史计划或外部服务初始化当成评测结果。

**当前门槛：** 高风险全量 Case 和低风险标签选择都必须可重跑并得到结构化报告；报告必须保留 Case/run/evaluator/evidence 关联。合成 Case 的 `expected_results.result` 已有字段级断言，真实业务质量仍不在本轮范围内。

### P1：按目标部署补生产身份与真实 Eino 恢复证据

- 若目标是给团队或客户长期运行：先确定身份提供方、租户/资源模型和运行环境，再设计 OIDC/JWT、RBAC/资源授权与审计，不先做通用权限平台。
- 若计划用 `eino_adk` 作为正式执行器：增加真实模型驱动 ToolCall 和跨进程 checkpoint/resume 的受控集成证据；记录模型/适配器版本、测试环境和失败语义。短期不能做到时保持 `partial`。
- 文件存储若仍是本地单实例足够，不需要为“成熟度”提前换数据库；如果开始多副本运行，优先替换存储和 per-run 执行租约，并验证原子状态推进、事件追加与恢复协调。

### P2：生产运行能力（以需求/指标触发）

- 多实例执行、数据库级事务/迁移、共享租约/队列、备份与保留、密钥管理、告警/SLO、限流/配额、部署/回滚。
- 评测集和回归门禁：当前已有路由、拒绝、审批、幂等和稳定性基线；后续按真实需求补结果字段、Tool 参数、摘要质量、延迟和 token/cost 基线。Judge/在线模型评估只补充本地合同。
- 长期记忆或知识库：先定义数据来源、隔离、更新/删除、TTL、召回质量和隐私边界，再选搜索/向量方案。短期 Memory 不能直接扩成知识平台。

### P3：仅在真实工作流需求出现后扩编排

并行/DAG、混合 ReAct/确定性工作流、动态用户编排、工具市场、画布和版本发布，会引入拓扑校验、节点版本兼容、并发副作用、恢复语义和管理面。先用具体任务及容量/延迟指标决定边界；不要仅为了追平 Coze/Dify 的 feature list 建平台。

## 参考来源与本地对比代码

以下只列本轮实际读取的本地参考；未列出的上游项目没有被当作实现证据。

| 项目 | 固定 commit | 来源与对比入口 |
| --- | --- | --- |
| CloudWeGo Eino | Go module cache `v0.9.12`；`compose/checkpoint.go`、`adk/runner.go` | Runner checkpoint/resume 原语；本项目只把它作为 infrastructure 适配器 |
| Coze Studio | `../agent-architecture-references/coze-studio/`，本地 commit `fefb05ff`；execute history repository | 条件认领和恢复状态；本项目实现最小 Run lease |
| ecommerce-customer-service-agent | `../agent-architecture-references/ecommerce-customer-service-agent/`，本地 commit `0c31ebb`；order repository/models | checkpoint identity 和幂等键；不引入其 Python 运行时 |
| Temporal Go SDK / LangGraph | 当前工作区没有源码或模块缓存 | 只作为待验证概念基线，状态为 `deferred` |

本次不复制参考源码进入当前仓库，也不引入运行依赖。

## 建议的“已完成”定义

能力描述按以下层级区分，不用“支持”一个词混淆：

- **代码存在**：实现文件或接口存在。
- **合同通过**：本地 fake/单元测试覆盖输入、拒绝和状态不变量。
- **集成通过**：composition 与 HTTP/SSE 入口跑通。
- **生产语义已证实**：目标身份、真实适配器、重启/多实例/故障场景在声明环境中有可复现证据。

当前核心状态大致为：多数安全/状态能力已达到合同或集成级；文件存储、静态身份、Memory 和 Eino resume 仍有明确运行边界；合成业务只有 fixture 级证据；生产级和平台级能力不可据此推定。
