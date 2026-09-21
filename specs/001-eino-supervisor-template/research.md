# Research Decisions

## R1：沿用 Go/Eino 与现有运行模式

选择 Go 1.25、Eino、Gin 和 HTTP/SSE。目标是复用现有团队对 Supervisor、Manager、Runner、事件和恢复的理解，减少新语言与新运行时带来的上下文成本。

## R2：v1 只使用内存实现和 fake 能力

模板的目的，是固定业务合同和依赖方向，不是提前搭建运营平台。内存 Repository、内存 checkpoint 和 fake Tool 足以验证状态、审批、幂等和事件规则；CRM、数据库、消息队列和真实营销渠道留到有真实需求时再引入。

## R3：LLM 只产生候选决策

模型输出必须经过结构化解析、注册表检查和 Policy Gate。模型不能授权 Tool、改变审批状态、生成幂等键或判断终态。这样模型替换、输出波动和 prompt 修改都不会改变安全边界。

## R4：先定义 durable-shaped 合同

v1 不接持久化数据库，但提前固定 `ExecutionPlan`、`Checkpoint`、`ApprovalRequest` 和 `RunEvent` 的最小字段与状态。以后替换存储实现时，领域合同和恢复语义不需要重写。

## R5：Spec Kit 管理变更流程

`.specify/memory/constitution.md` 是唯一项目宪法；Spec Kit 的 specify、plan、tasks、implement、converge 负责变更流程和产物生成。不得再建立第二份宪法或让配置、Prompt 成为隐式规则来源。

## R6：Gin 作为 Web 边界，参考 go-porter 的基础设施装配

模板使用 Gin 承担 HTTP、SSE、middleware、健康检查和优雅关闭。参考 `/home/huang/workspace/go-porter` 的配置入口、ServiceContext 装配、middleware 组合和 server 生命周期；只移植机制，不复制其业务 handler、认证、MySQL/Redis 默认依赖，也不让模板依赖 go-porter 包。这样能复用已验证的 Go Web 习惯，同时保持模板独立和领域合同可测试。

## R7：配置优先，但配置不能改变安全合同

部署参数、模型参数、Prompt 路径、能力开关、工具白名单、预算、超时、重试、审批和观测参数通过 TOML 与环境变量配置。加载顺序固定为 `-f 文件 -> TOML -> 环境变量覆盖 -> 启动校验`，配置错误在启动时失败，v1 不做热加载。安全不变量、Schema、状态转换、权限上限、Policy Gate、审批不可绕过规则和状态所有权由代码固定；配置不能创造未注册实现、扩大权限或降低安全要求。

## 未采用的方案

- 不复制完整平台的工作流 DSL、插件市场、多租户和 RBAC；它们超出模板验收范围。
- 不引入第二套 Agent 编排框架；Eino 作为基础设施适配，领域合同保持框架无关。
- 不让 SSE handler 直接推进业务状态；传输层只投影内部事件。
