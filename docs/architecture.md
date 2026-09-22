# Architecture

## 目的与范围

这是一个通用的运营 Agent/Supervisor 参考模板，用来验证有界执行、策略门控、审批、恢复和事件投影的合同。它不包含命理业务，不连接真实客户数据、营销渠道或外部运营平台。

本文件是目录职责和依赖方向的项目事实来源；项目宪法仍是更高优先级的约束。实现、计划和任务必须引用本文件，不得在其他文档中另写一套目录规则。

## 固定运行链路

所有请求必须遵循这一条有界链路：

`HTTP/SSE + AuthN -> Application + resource AuthZ -> Supervisor -> Policy Gate -> Manager / ExecutionPlan -> bounded Worker -> allow-listed Tool -> Final Guard -> RunEvent -> SSE Projection`

各环节的可执行规则如下：

1. `HTTP/SSE + AuthN` 只解析请求、验证 Bearer 凭证并建立响应流；健康检查公开，`/api/*` 必须携带可信 `Subject`。
2. `Application + resource AuthZ` 负责用例编排和运行入口；创建 run 绑定主体，重放、审批、恢复和取消先校验 run 所有者。
3. `Supervisor` 只提出结构化候选路由；它不能授权能力、修改状态、生成幂等键或决定终态。
4. `Policy Gate` 依据注册表、风险、权限和输入合同确定性地产出允许或拒绝结果。它管理模型提出的 Worker/Tool 动作，不替代资源所有权校验。
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

任何反向依赖都必须先改变设计文档并通过宪法要求的变更流程。`composition` 只装配实现、注册能力和注入依赖，不承载业务判断；基础设施实现只能通过领域或应用合同接入。能力只在启动期显式注册，配置只能引用已注册 ID；重复 ID、缺失引用、allow-list 越权和降低审批要求的配置必须在启动时失败。禁止反射扫描、`init()` 自注册、热加载和通用 `Component` 接口。

## 目录职责与禁止事项

| 目录 | 负责内容 | 明确禁止 |
| --- | --- | --- |
| `cmd/` | 进程入口、命令行参数、启动校验、优雅关闭 | 领域规则、Supervisor 决策、Tool 调用和 SSE 业务判断 |
| `internal/domain/` | `Subject`、`SupervisorDecision`、`ApprovedRoute`、`ExecutionPlan`、`WorkerContract`、`ToolContract`、`RunEvent`、审批和状态不变量 | 验证凭证，依赖 Gin、Eino、数据库、MCP、具体模型、SSE 或传输协议 |
| `internal/application/` | Authenticator/RunAuthorizer 合同、Chat、Run、Approval 等用例；Manager 持有运行状态和 `ExecutionPlan` | 直接绑定 Gin 类型、把认证 SDK 当领域规则、让模型决定权限、把传输层当状态 owner |
| `internal/interfaces/` | Gin HTTP 请求适配、SSE 输出和公开错误映射 | 绕过 Application、直接调用 Tool、推进状态或重新生成终态 |
| `internal/infrastructure/` | Eino、模型、认证/资源授权适配器、内存/持久化存储、checkpoint、Tool、MCP、日志和观测适配器 | 定义新的业务所有权、绕过 Policy Gate 或把外部响应直接当最终答复 |
| `internal/composition/` | 注册表、实现选择、依赖注入和运行装配 | 业务流程、隐式全局状态和绕过合同的快捷调用 |
| `configs/` | 后续可放部署配置样例；当前 v1 以根目录 `config.example.toml` 为入口，目录可以不存在 | 密钥、运行时代码、业务规则或修改权限上限；新增顶层目录仍须遵守宪法 |
| `docs/` | 架构、技术基线、数据流和验收说明 | 生产代码、隐式运行规则和与架构事实冲突的副本 |
| `specs/` | Feature Spec、Plan、Research、Data Model、Contract、Tasks | 把计划状态写成代码完成，或绕过宪法定义新规则 |

`internal/domain/` 的隔离是硬边界：替换 Gin、Eino、数据库、MCP、模型或 SSE 时，领域合同和含义不能改变。`Manager` 是唯一的运行状态和 `ExecutionPlan` owner；Worker 不能发布最终答复，最终答复必须经过 `Final Guard` 和统一事件投影。

所有 Tool 都必须先进入注册表和 allow-list，再接受 `Policy Gate` 检查。配置只能选择已注册实现，不能新增 Worker/Tool、扩大权限、降低审批等级或关闭门禁。项目运行依赖只来自已声明并经过验证的组件。

## v1 实现边界

v1 默认使用内存 Repository、内存 checkpoint、fake model 和 fake Tool，目标是验证合同、状态和事件顺序；M4 已补充可选的文件持久化实现，支持单实例写入和跨进程读取恢复快照，但不承诺数据库级高可用、多主并发或跨存储 exactly-once。执行结果在外部调用后、提交前发生进程中断时必须视为未知，恢复返回 `RUN_OUTCOME_UNKNOWN`，禁止自动重试。M3 已补充可选真实 ChatModel、固定地址 HTTP 只读 Tool 和 OpenTelemetry 适配，M4 还补充了受 allow-list、超时、大小限制和错误分类约束的 MCP Tool 适配；这些都不会改变默认本地合同路径。第一条可运行验收链路固定为：

`Gin -> Application -> Eino Supervisor -> fake Worker -> fake Tool -> RunEvent -> SSE`

本轮 M4 已补充 T016 持久化 checkpoint、T017 受控 MCP Tool 和 T018 的最小身份/资源隔离边界；完整 OIDC/JWT、RBAC、组织层级和具体运营业务仍延期。默认认证器只把配置的静态 Bearer 哈希映射为主体，run 资源暂按创建主体隔离。

## 身份与权限边界

本项目只借鉴本地 Coze Studio 的边界，不复制其平台实现：认证中间件先建立可信 session/主体，资源授权再按操作者与资源校验。模板中对应为：

1. `Authenticator` 把 HTTP Bearer 凭证映射为 `identity.Subject{TenantID, SubjectID}`；主体不从请求 JSON 读取。
2. `RunAuthorizer` 只判断主体是否拥有目标 run；未知主体、跨租户或跨用户的 run 操作不能改变状态。
3. `PolicyEvaluator` 仍只处理 Supervisor 提出的 Worker/Tool 动作和风险，不承担用户资源授权。
4. 审批人由认证主体的 `SubjectID` 写入，不接受客户端 `reviewer` 字段。

该最小实现用于本地合同和边界验证，不是完整身份平台；进入多租户生产部署前再替换认证器和资源授权器，并保持上述应用层分层。

## 脚手架扩展约束

本项目定位为 Agent 开发脚手架，不定位为 Coze 类工作流平台。稳定内核包括 HTTP/SSE、应用运行状态、ExecutionPlan、Policy Gate、审批、幂等、Final Guard、checkpoint 和事件投影；业务实现、路由组装和执行策略属于可替换扩展。

新增业务 SHOULD 集中在独立业务模块，通过 Catalog/注册表和配置接入。路由、Worker/Expert、Prompt、Tool 绑定、Runner 选择和预算优先由配置描述；配置只能选择已注册实现，不能创造能力、扩大权限或改变状态所有权。

应用层应依赖稳定的 WorkerRunner 合同。普通函数 Worker、Eino ReAct、Graph 或其他执行器应作为基础设施适配器接入；更换执行器不应修改 HTTP、Policy Gate、状态机、通用运行循环和 SSE 投影。Runner/Tool 必须遵守 `context.Context`，取消是协作式语义，不对外承诺强制终止任意外部进程。

组件化增量的具体阶段、注册合同、故障边界和开源源码依据见 [脚手架演进先行方案](./scaffold-evolution-plan.md)
和 [组件参考矩阵](./component-reference-map.md)。`ExecutionPlan` 是业务状态唯一 owner，checkpoint 只做恢复投影，RunEvent 只做事件投影；三者之间没有隐式万能事件总线或跨存储事务假设。

当前实现仍有一项已知偏差：`application/run` 直接执行批准 Tool，尚未通过真实 WorkerRunner；配置中的路由、Prompt、Runner 和部分预算字段也尚未形成统一 Runtime Catalog。该偏差由 [脚手架演进先行方案](./scaffold-evolution-plan.md) 分阶段收敛，不应在文档中提前描述为已完成能力。
