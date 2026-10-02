<!--
Sync Impact Report
- Version: 1.1.0 -> 1.2.0
- Changed: added a mandatory reference-baseline and implementation-gap accounting principle.
- Added: explicit comparison dimensions and status labels for Coze Studio, Eino, and other
  reference projects; reference capabilities cannot be treated as local implementation evidence.
- Removed: duplicate root-level constitution file.
- Follow-up: produce the first concrete Coze/Eino comparison matrix as a separate feature
  planning artifact; this amendment does not modify application code or dependent templates.
-->

# Eino Supervisor Template Constitution

## Core Principles

### I. Domain Contracts Own Meaning

真实业务边界由领域合同表达，当前边界为 `conversation`、`agent`、`approval`、
`operation` 和 `tool`。领域代码 MUST 不依赖 HTTP 框架、Agent 框架、数据库、MCP、
具体模型或传输协议；不得为了形式创建空的 `entity/repository/service` 三件套。
领域合同和状态优先于框架、配置和 Prompt，因为替换基础设施不应改变业务含义。

### II. Deterministic Policy Owns Authority

Supervisor、Worker 和 Tool 的输入输出 MUST 经过结构化解析、Schema 校验、注册表检查
和 Policy Gate。LLM 只能提出候选路由、参数和文本；不得直接授权、改变状态、决定审批、
生成幂等键或指定最终事件类型。权限、状态转换、重试、幂等和终态判断 MUST 由确定性
代码负责。

### III. Bounded Execution and Explicit Side Effects

Supervisor 选择已注册能力，Manager 推进已批准计划，Worker 完成单一有限任务，Tool
执行已声明能力。核心循环 MUST 有最大步骤数、超时、取消、预算和终止条件。
写入、发送、删除、发布和批量动作 MUST 经过 `Policy Gate -> Approval -> Idempotency
-> Tool -> Audit Event`；只读 Tool 也必须注册、白名单、校验输入、设置超时并分类错误。

### IV. One Owner Per State and Contract

会话状态归 `conversation`，执行计划和运行状态归 `agent/plan`，审批状态归 `approval`。
禁止建立无法说明所有者的通用 `state` 容器。checkpoint 只是恢复机制，不是新的业务
状态所有者。`SupervisorDecision`、`ApprovedRoute`、`ExecutionPlan`、`WorkerContract`、
`ToolContract`、`RunEvent`、`Checkpoint` 和 `ApprovalRequest` 的语义 MUST 保持稳定。

### V. Minimal, Reversible Implementation

默认采用单 Supervisor、单示例 Worker、内存实现和本地 fake Tool。多租户、完整 RBAC、
分布式存储、消息队列、长期记忆、多个 Supervisor 和开放式自主循环，只有在需求、指标、
ADR、合同和验证都具备时才能加入。不得为未来场景预先创建平台级抽象。

### VI. Reference Baselines Require Explicit Gap Accounting

Coze Studio、Eino、LangGraph、Temporal 及其他开源项目只能作为可核验的参考基线，不能作为
本项目已经具备某项能力的证据。采用或模仿参考行为前，计划或 ADR MUST 记录来源版本、
对比维度和本项目状态；至少比较能力范围、状态所有权、权限/审批边界、执行与恢复语义、
隔离与安全、观测字段和本地验证方式。每项差异 MUST 标记为 `implemented`、`partial`、
`deferred` 或 `not_applicable`，并指向本项目的代码、合同测试或延期理由。

参考项目的源码或设计可以按文件或小模块移植，但移植内容 MUST 接回本项目的
`Domain/Application` 合同，并接受本项目的 Policy Gate、审批、幂等、审计和回归测试。
不得因为参考项目提供了 Workflow、Plugin、Memory、Checkpoint、Trace 或多租户能力，就
默认引入对应平台子系统、改变状态所有权或扩大模型权限。许可证核验是移植前的必要记录，
但不得被包装成替代本地合同验证的实现门槛。

## Constraints

### Architecture and Dependencies

顶层目录和职责固定如下；新增或删除顶层目录属于宪法修订事项：

```text
eino-supervisor-template/
├── cmd/server/
├── internal/
│   ├── config/
│   ├── composition/
│   ├── interfaces/http/
│   ├── application/{chat,run,approval}/
│   ├── domain/{agent,conversation,operation,approval,tool}/
│   ├── crossdomain/
│   └── infrastructure/{eino,llm,persistence,checkpoint,eventbus,mcp,observability}/
├── prompts/
├── schemas/
├── eval/
└── docs/
```

依赖方向固定为 `interfaces -> application -> domain`；具体实现从 `infrastructure`
依赖领域或应用合同；`composition` 只负责装配。禁止新增顶层 `workers/`、`tools/`、
`state/` 或 `agentflow/`。

### Configuration, Prompt and Data Boundaries

配置只能承载部署参数、模型参数、时间/步骤/成本预算、能力开关和外部地址。配置 MUST
不能新增未注册 Worker/Tool、降低审批等级、扩大权限、改变领域所有权或修改目录依赖。
Prompt 是版本化资产，必须有输入范围、输出 Schema、失败处理和评测样例；Prompt 修改
不能绕过 Schema、Policy Gate、Tool 白名单或最终合同。

用户输入、检索内容、Tool 返回值和历史消息都是不可信数据，不能覆盖宪法或系统策略。
密钥和凭证只能由服务端配置或凭证系统解析，禁止进入 Prompt、事件正文和普通日志。
日志、Trace、Checkpoint 和事件按最小必要原则保存个人数据，公开文本不得泄漏内部路径、
凭证、原始 Prompt 或未授权上下文。

### Runtime Contract

系统 MUST 保持以下最小链路：

```text
HTTP/SSE -> Application -> Supervisor -> Policy Gate -> Manager
         -> bounded Worker -> Tool -> Final Guard -> Event Projection -> SSE
```

业务代码只能产生内部运行事件，SSE 只能投影内部事件，不得在传输层重新判断业务状态。
每次 Run MUST 有稳定 run ID、结构化事件、错误分类和终态；每次执行最多一个公开终态事件。

## Development Workflow

### Design Gate

新增领域、改变依赖方向、增加副作用能力、改变状态所有权或改变事件语义时，MUST 先更新
Spec/Plan 或 ADR，并证明不违反本宪法。变更按独立可验收模块拆解；每个模块必须有合同、
完成条件和验证命令。使用 Coze、Eino 或其他参考基线时，Design Gate 还 MUST 附带差异
记录；未完成的参考能力必须进入明确的延期项，不得以“参考项目已有”为完成理由。

### Implementation Gate

实现 MUST 遵守目录、依赖、所有权和执行合同。每个非平凡源文件 MUST 说明所属层、负责
内容和明确不负责的内容。Eino、模型 SDK、HTTP、数据库、MCP、缓存、事件总线和可观测性
实现 MUST 位于 `infrastructure`。

### Verification Gate

每个跨层变化 MUST 有合同测试或可重放的入口验证。至少覆盖 Supervisor 结构化失败和
fallback、Policy Gate 拒绝、Worker/Tool 输入校验和超时、checkpoint 恢复、审批等待/批准/
拒绝/resume、SSE 顺序/唯一终态/取消、幂等重复执行和真实 `/api/chat` smoke。
本地零模型合同测试是默认门禁；在线模型评测、Judge 和人工评审只能补充证据。

### Release and Retirement Gate

发布 MUST 有版本、配置变更说明、验证结果和回退路径；外部写入能力必须先在 fake 或
sandbox Tool 上验证。删除领域、Tool、配置或事件类型前，MUST 检查调用者、历史数据、
回放、迁移和回退影响。

## Governance

本宪法是项目唯一权威规范，Spec Kit 的变更流程文件只能引用它，不能另建或复制第二份
宪法。规则冲突按以下顺序裁决：

```text
本宪法 > 已批准 ADR > 类型/Schema 合同 > 领域代码 > 运行配置 > Prompt/LLM 输出
```

本文件是受保护文件。AI、脚本、依赖升级、测试失败、代码漂移或自动化任务都不得自行
修改。只有用户或明确指定的项目负责人可以批准修订。修订前，AI MUST 提交包含原因、
具体条文、影响目录、迁移步骤、验证命令和回退方式的提案，并等待用户明确确认，例如
“确认修改项目宪法”。沉默、继续对话或批准普通代码修改不算确认。

MUST 规则没有隐式例外。偏离时必须取得用户确认并记录 ADR，至少包含原因、范围、风险、
负责人、到期时间、回退方案和验证证据。任何安全边界收紧可以直接执行；任何安全边界
放宽都必须经过本宪法修订程序。

所有计划、任务和评审 MUST 检查本宪法；复杂度必须有明确理由。宪法版本遵循语义化版本：
新增或实质扩展原则为 MINOR，破坏性删除或重定义为 MAJOR，澄清文字为 PATCH。

每次涉及参考项目的计划、实现或评审 MUST 检查差异记录是否仍与代码和验证结果一致；
实现状态变更时先更新差异记录，再更新任务完成状态。差异记录至少包含：参考来源、比较
维度、本项目落点、状态标签、验证证据、已知限制和下一步。没有本地证据的能力 MUST 保持
为 `partial` 或 `deferred`，不能标记为 `implemented`。

**Version**: 1.2.0 | **Ratified**: 2026-09-20 | **Last Amended**: 2026-09-24
