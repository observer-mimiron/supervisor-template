# Agent 脚手架演进先行方案

## 1. 结论

当前架构已具备安全合同、运行可靠性和第一阶段组件化基础，示例业务模块已完成集中注册，仍保留真实业务和 Runner 扩展。

本方案是对现有 M0-M4 实现的组件化增量，不是重新建设一套 Runtime。当前批次已收敛
`Catalog -> WorkerRunner -> 集中示例业务模块`；记忆、复杂授权、分布式编排和动态插件也不在本阶段。
下文把恢复一致性、取消语义、幂等未知结果和注册生命周期写成硬合同，避免把“有接口”误写成“已经可靠”。

当前已经具备稳定内核的主要部分：

- HTTP/SSE 适配、运行状态、事件顺序、唯一终态和 checkpoint。
- Supervisor 决策、Policy Gate、审批、幂等和 Final Guard。
- Gin、Eino、模型、Tool、存储和观测的基础设施边界。
- fake、文件存储、HTTP 只读 Tool 和受控 MCP Tool 的替换路径。

当前仍保留的结构性缺口：

1. 当前默认 Runner 是单 Tool 适配器；Eino ReAct/Graph 尚未接入应用主链路。
2. 真实运营业务仍不在模板范围内。

因此，项目后续目标仍不是建设工作流平台，而是补齐：

```text
稳定内核 + Catalog + 可替换 WorkerRunner + 集中业务模块
```

## 2. 架构合理性评估

### 2.1 合理的部分

依赖方向基本正确：

```text
interfaces -> application -> domain
composition -> application + domain + infrastructure
infrastructure -> domain/application contracts
```

领域层没有依赖 Gin、Eino、模型、数据库、MCP 或 SSE，符合替换基础设施不改变业务含义的要求。

运行状态由 `application/run.Service` 持有，审批由审批合同表达，事件由 EventStore 顺序约束，当前所有权划分清楚。单进程全局锁虽然限制吞吐，但对 v1 的幂等和重复 resume 保护是可解释的实现取舍。

配置也已经有安全边界：只能选择已注册实现，不能扩大权限、降低审批等级、绕过 Schema 或关闭 Policy Gate。

### 2.2 需要修正的部分

#### A. 运行链路边界（已收敛）

`run.Service` 已固定调用 `WorkerRunner`；默认 `SingleToolRunner` 内部才映射为一次 `ToolExecutor` 调用。

当前状态：应用层只负责创建有界计划、校验批准路由、调用 `WorkerRunner` 并收口结果；fake Worker 仍通过同一合同执行一次 Tool。

#### B. 配置目录（已收敛基础边界）

启动配置已编译为不可变 `RuntimeCatalog`，并校验 Prompt、Runner、Tool、路由、allow-list 和预算引用。

保留边界：Catalog 只选择已注册能力，不通过配置创造新实现，也不替代领域状态规则。

#### C. 业务注册（已收敛）

`internal/infrastructure/examplebusiness` 已集中保存 `user_analysis`、`user_query`、`simulated_outreach` 的 Worker、Route、Prompt、Tool 绑定和安全合同；通用 Tool/LLM 装配只消费注册描述。

当前状态：业务能力、Prompt、Tool 绑定和注册描述集中在明确模块；composition 启动时拒绝未注册的 Worker、Route 或 Tool。

#### D. 执行器替换点（基础合同已完成）

应用层已提供稳定的 `WorkerRunner` 合同，当前装配使用 `SingleToolRunner`。

后续方向：ReAct、Graph、函数式 Worker 继续作为基础设施适配器实现该合同；更换执行器不改变 HTTP、Policy、状态机和事件投影。

## 3. 目标边界

### 3.1 配置负责什么

配置负责组装已经注册的能力：

- `task/intent -> worker` 路由。
- Worker 使用的 Runner 类型。
- Worker Prompt 文件。
- Worker 允许的 Tool。
- Tool 实现、endpoint、超时和重试。
- 步骤、调用次数、成本和超时预算。
- 模型、存储、事件和观测实现选择。

### 3.2 代码负责什么

代码负责能力本身和不可绕过的安全边界：

- 业务算法、外部系统适配和输入输出 Schema。
- 已注册 Worker、Runner 和 Tool 的实现集合。
- Policy Gate、审批、幂等、状态转换和终态规则。
- Tool 调用的权限上限、参数校验和错误分类。
- 事件结构、checkpoint 合同和公开文本 Final Guard。

准确目标不是“只写 TOML 就能产生任意业务”，而是：

> 新增业务只写业务模块、Prompt、注册描述和配置；通用运行内核不改或只改应用合同。

### 3.3 注册与生命周期合同

组件注册只发生在进程启动的 `composition` 装配阶段。每个可选实现拥有稳定、大小写敏感的
类型 ID，配置只能引用已经编译进程序的 ID，不能通过字符串反射、`init()` 副作用或运行时
热加载创建新能力。

启动校验必须拒绝以下情况：重复 ID、缺少实现、缺少 Prompt/Tool 引用、Worker 的 Tool
不在 allow-list、路由引用未启用能力，以及配置试图降低代码固定的权限或审批要求。校验成功
后生成不可变 `RuntimeCatalog`；运行期间不再读取 TOML map，也不允许修改注册表。

不引入通用 `Component` 接口。只有确实有多个实现时，才为该能力定义窄的、类型明确的
Registry/Factory；只有拥有外部资源的实现才接入进程级关闭流程（例如通过显式 `Close`），
不为所有对象预先创建生命周期框架。

### 3.4 v1 可靠性合同

`ExecutionPlan` 是业务状态唯一来源；`Checkpoint` 是恢复投影；`RunEvent` 是公开事件投影。
三者共享 `run_id`，但不能互相取代所有权。

每个步骤遵循以下顺序：

1. 保存请求、计划和 `running` 状态；
2. 保存可恢复 checkpoint 后，才允许调用外部 Worker/Tool；
3. 调用成功后，先保存带有幂等键和结果摘要的执行记录，再写成功/终态事件；
4. 事件写入失败时，可根据计划和 checkpoint 重建缺失投影；事件追加必须按稳定事件 ID 幂等；
5. 如果进程在外部调用之后、结果提交之前退出，结果视为“未知”，恢复流程必须返回
   `RUN_OUTCOME_UNKNOWN` 并等待人工核对，禁止自动重试。v1 不宣称跨进程 exactly-once。

取消是协作式语义：Runner/Tool 必须遵守 `context.Context`。系统只承诺在调用尚未完成或
调用方观察到取消后收口为 `canceled`；不承诺强制杀死任意外部进程。应用锁不能覆盖不可控的
外部调用；重复 resume 通过每个 run 的执行租约或等价串行保护实现。文件实现只支持单实例
写入和崩溃可恢复，不承诺多主并发或数据库级高可用。

## 4. 目标结构

```text
HTTP/SSE
  -> application/run
    -> Supervisor Decision
    -> Policy Gate
    -> ExecutionPlan
    -> WorkerRunner(worker_id, runner_id)
      -> policy-bound ToolInvoker
      -> typed WorkerResult
    -> Final Guard
    -> RunEvent
  -> SSE Projection

config -> Catalog compiler -> immutable RuntimeCatalog
business module -> registered Worker/Tool/Runner implementation
```

建议保持现有顶层目录，不新增 `workers/`、`tools/`、`state/` 或平台级 DSL。业务模块可以放在 `internal/infrastructure` 下的明确业务包，或放在现有领域/基础设施包中；具体落点以第一项业务接入时的依赖关系决定。

最小应用合同建议如下，先只覆盖当前单 Worker、单有界任务语义：

```go
type WorkerRunner interface {
	Run(context.Context, WorkerRequest) (WorkerResult, error)
}
```

`WorkerRequest` 必须包含 run、worker、intent、结构化参数、允许工具、deadline 和幂等上下文；`WorkerResult` 只能返回结构化结果和受控 Tool 调用结果，不能直接发布最终事件。多步骤 ReAct/Graph 只有在需要时扩展合同，不提前建设完整工作流抽象。

## 5. 最小实施阶段

### Phase 0：锁定扩展规则

产出：

- 本方案作为评审依据。
- 在 `docs/architecture.md` 和 `specs/001-eino-supervisor-template/plan.md` 中同步“业务集中、Catalog、Runner 可替换”约束。
- 如需把三条原则提升为 MUST，再提交宪法修订提案；未获得明确确认前不改宪法。

完成信号：文档不再把“已有配置字段”描述成“已完整驱动运行”。

### Phase 1：Catalog 编译（已完成）

产出：

- typed `Route/Worker/Tool/Runner` 描述。
- 启动时统一解析引用、实现 ID、Prompt、allow-list 和预算。
- 运行时只使用不可变 Catalog，不读取配置 map 和业务字符串。

完成信号：新增一个路由或替换一个 Prompt 只需改配置和业务注册，不改 `run.Service`。

必须同时完成：重复注册、缺失引用、allow-list 越权、配置降低审批等级的启动失败测试，
以及 Catalog 编译后运行期不可变的合同测试。

### Phase 2：WorkerRunner 接入（已完成）

产出：

- 应用层 `WorkerRunner` 合同。
- 当前 fake Worker 的最小适配器。
- `run.Service` 从直接 Tool 调用改为调用 Runner。
- 现有审批、幂等、Final Guard、checkpoint 和事件语义保持不变。

完成信号：应用层不再知道 fake、Eino、ReAct 或 Graph 的具体类型。

必须同时固定 `WorkerRequest` 的幂等键、deadline、允许 Tool/策略版本和 attempt 上下文；
增加协作式取消、调用后未知结果和 Runner 错误分类测试。Runner 不得直接发布 `RunEvent`。

### Phase 3：集中一个示例业务（已完成）

本阶段已在 C018 完成，不改变运行状态 owner 和传输层合同。

产出：

- 一个独立示例业务模块。
- 该模块集中保存 Worker 实现、Prompt、Tool 绑定和注册描述。
- 删除通用 Registry 中针对示例 ID 的业务 `switch`。

完成信号：只增加一个业务模块、Prompt、注册描述和配置，就能通过 `/api/chat` 完成路由和执行。

业务注册测试证明核心 `run.Service`、HTTP handler、Policy Gate 和 SSE 投影没有被修改；
composition 会拒绝不在显式业务模块中的能力引用。

### Phase 4：补 Runner 替换验收

产出：

- fake Runner 合同测试。
- 一个最小函数式 Runner 替换测试。
- ReAct/Graph 只做适配器设计或最小 proof，不建设 DSL、编排后台和插件市场。

完成信号：替换 Runner 不修改 HTTP、Policy、状态机、SSE 和通用运行主循环。

Phase 4 只验证一个最小函数式 Runner 替换，不建设 ReAct/Graph DSL、插件市场或热加载平台。

## 6. 验收标准

必须有一个“业务接入测试”验证以下事实：

1. 新增一个 Worker/Route/Prompt/Tool 组合。
2. 不修改 `internal/application/run`、HTTP Handler、Policy Gate 和 SSE Projection。
3. 启动校验能发现缺失实现、Prompt、Tool 或 allow-list 引用。
4. `/api/chat` 能真实触发新路由并返回唯一终态。
5. 只读 Tool、审批 Tool、超时、取消和重复 resume 仍遵守原合同。
6. Runner 替换只改变 composition 和业务适配器。

还必须覆盖以下故障边界：

7. 计划已保存但事件追加失败时，重启/重放不会产生第二个终态；
8. 外部调用完成但结果提交未知时，系统返回 `RUN_OUTCOME_UNKNOWN`，不自动再次调用；
9. Runner 遵守 context 时，取消最终只产生一个 `canceled` 终态；不遵守 context 的外部进程
   不被伪装成“已强制取消”；
10. 两个相同组件 ID 或一个未注册 ID 无法通过启动校验。

建议保留的基础验证：

```bash
go test ./...
go test -race ./...
go build ./cmd/server/
go vet ./...
git diff --check
```

还需要增加的专项验证：

- 配置路由到不同 Worker 的合同测试。
- Runner 替换测试。
- 业务模块不修改核心文件的变更边界检查。
- 真实 `/api/chat` SSE smoke，检查事件顺序和唯一终态。

## 7. 明确暂不做

- 不复制 Coze 的完整 Workflow DSL、插件市场或管理后台。
- 不提前设计通用 Graph 节点协议、动态发现和热加载。
- 不允许 Prompt 或 TOML 改变权限、审批、状态所有权和终态。
- 不把所有业务都抽象成一个万能 Worker。
- 不在本阶段引入多租户、分布式调度和开放式自主循环。
- 不承诺跨进程 exactly-once、强制终止任意外部进程或多主并发文件写入；这些需要独立的
  持久化/执行协调设计、指标和 ADR。
- 不把 Coze 的发布资源、插件 OAuth/版本生命周期、Knowledge 文档索引或复杂 Memory
  召回语义提前实现；它们分别属于生产资源管理、外部身份、检索系统和长期记忆适配。

## 8. 与基线项目的取舍

- Coze：借鉴 Workflow 的配置/运行时分离、Plugin 的认证与资源生命周期、Memory 与
  Conversation 分离、Knowledge 与记忆检索边界；本项目只落成 `PlanStep`、`ToolPool`、
  `MemoryStore` 和既有认证/资源授权合同，不复制平台复杂度。
- `suanming-agent`：重点借鉴 `Catalog`、`task -> work -> owner`、启动校验和路由摘要。
- Gin 项目：只借鉴 HTTP、SSE、启动和中间件适配，不让 Gin 参与 Agent 核心设计。

开源源码级参考见 [组件参考矩阵](./component-reference-map.md)。矩阵固定仓库、commit、
源码区域、借鉴点和不采用部分；它是研究证据，不是运行时源码供应目录。除非后续任务明确
需要补丁，否则不把完整上游仓库复制到本项目，也不新增顶层 `lib/` 目录。

## 9. 评审结论

当前架构不需要推倒重来。它的稳定内核、状态合同和安全门禁值得保留；需要做的是把“业务执行”和“基础设施装配”从固定示例中抽出来。

最小正确顺序是：

```text
先统一 Catalog -> 再引入 WorkerRunner -> 再集中示例业务 -> 最后验证 ReAct/Graph 替换
```

如果跳过 Catalog 直接接入 ReAct/Graph，复杂度会进入 `run.Service`；如果先做完整 DSL，项目会从脚手架膨胀成平台。
