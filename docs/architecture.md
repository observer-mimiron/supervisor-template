# Architecture

## 目的与范围

这是一个通用的运营 Agent/Supervisor 参考模板，用来验证有界执行、策略门控、审批、恢复和事件投影的合同。它不包含命理业务，不连接真实客户数据、营销渠道或外部运营平台。

本文件是目录职责和依赖方向的项目事实来源；项目宪法仍是更高优先级的约束。实现、计划和任务必须引用本文件，不得在其他文档中另写一套目录规则。

## 固定运行链路

所有请求必须遵循这一条有界链路：

`HTTP/SSE -> Application -> Supervisor -> Policy Gate -> Manager / ExecutionPlan -> bounded Worker -> allow-listed Tool -> Final Guard -> RunEvent -> SSE Projection`

各环节的可执行规则如下：

1. `HTTP/SSE` 只解析请求、建立响应流和投影事件，不推进业务状态。
2. `Application` 负责用例编排和运行入口，接收不可信输入并调用领域合同。
3. `Supervisor` 只提出结构化候选路由；它不能授权能力、修改状态、生成幂等键或决定终态。
4. `Policy Gate` 依据注册表、风险、权限和输入合同确定性地产出允许或拒绝结果。
5. `Manager` 是运行期状态和 `ExecutionPlan` 的唯一 owner，负责把已批准路由变成有界步骤并推进状态。
6. `bounded Worker` 只完成一个已声明、有限时长和有限步骤的任务，不能拥有最终答复权。
7. `allow-listed Tool` 只能在 Worker 声明的允许集合和 `Policy Gate` 结果内调用；Tool 必须校验输入、超时并分类错误。
8. `Final Guard` 校验结构化结果、敏感信息、终态和公开文本合同；不合格结果只能转为分类失败，不能猜测替代结果。
9. `RunEvent` 是内部运行记录，携带稳定 `run_id`、单调 `sequence` 和结构化类型；每个 run 只能有一个公开终态。
10. `SSE Projection` 只把 `RunEvent` 转为客户端事件，不重新判断权限、状态或业务结果。

## 固定依赖方向

依赖方向固定为：

`interfaces -> application -> domain`

`composition -> application + domain + infrastructure`

`infrastructure -> domain/application contracts`

任何反向依赖都必须先改变设计文档并通过宪法要求的变更流程。`composition` 只装配实现、注册能力和注入依赖，不承载业务判断；基础设施实现只能通过领域或应用合同接入。

## 目录职责与禁止事项

| 目录 | 负责内容 | 明确禁止 |
| --- | --- | --- |
| `cmd/` | 进程入口、命令行参数、启动校验、优雅关闭 | 领域规则、Supervisor 决策、Tool 调用和 SSE 业务判断 |
| `internal/domain/` | `SupervisorDecision`、`ApprovedRoute`、`ExecutionPlan`、`WorkerContract`、`ToolContract`、`RunEvent`、审批和状态不变量 | 依赖 Gin、Eino、数据库、MCP、具体模型、SSE 或传输协议 |
| `internal/application/` | Chat、Run、Approval 等用例；Manager 持有运行状态和 `ExecutionPlan` | 直接绑定 Gin 类型、让模型决定权限、把传输层当状态 owner |
| `internal/interfaces/` | Gin HTTP 请求适配、SSE 输出和公开错误映射 | 绕过 Application、直接调用 Tool、推进状态或重新生成终态 |
| `internal/infrastructure/` | Eino、模型、内存/持久化存储、checkpoint、Tool、MCP、日志和观测适配器 | 定义新的业务所有权、绕过 Policy Gate 或把外部响应直接当最终答复 |
| `internal/composition/` | 注册表、实现选择、依赖注入和运行装配 | 业务流程、隐式全局状态和绕过合同的快捷调用 |
| `configs/` | 后续可放部署配置样例；当前 v1 以根目录 `config.example.toml` 为入口，目录可以不存在 | 密钥、运行时代码、业务规则或修改权限上限；新增顶层目录仍须遵守宪法 |
| `docs/` | 架构、技术基线、数据流和验收说明 | 生产代码、隐式运行规则和与架构事实冲突的副本 |
| `specs/` | Feature Spec、Plan、Research、Data Model、Contract、Tasks | 把计划状态写成代码完成，或绕过宪法定义新规则 |

`internal/domain/` 的隔离是硬边界：替换 Gin、Eino、数据库、MCP、模型或 SSE 时，领域合同和含义不能改变。`Manager` 是唯一的运行状态和 `ExecutionPlan` owner；Worker 不能发布最终答复，最终答复必须经过 `Final Guard` 和统一事件投影。

所有 Tool 都必须先进入注册表和 allow-list，再接受 `Policy Gate` 检查。配置只能选择已注册实现，不能新增 Worker/Tool、扩大权限、降低审批等级或关闭门禁。项目运行依赖只来自已声明并经过验证的组件。

## v1 实现边界

v1 默认使用内存 Repository、内存 checkpoint、fake model 和 fake Tool，目标是验证合同、状态和事件顺序，不承诺生产级持久化恢复或外部副作用。M3 已补充可选真实 ChatModel、固定地址 HTTP 只读 Tool 和 OpenTelemetry 适配，但不会改变默认本地合同路径。第一条可运行验收链路固定为：

`Gin -> Application -> Eino Supervisor -> fake Worker -> fake Tool -> RunEvent -> SSE`

持久化 checkpoint、MCP、认证、多租户和具体运营业务模块仍属于 M4 后续范围，不能提前写成当前已具备的能力。
