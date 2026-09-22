# 组件参考矩阵

本文件记录组件化方案所依据的公开源码。它用于学习和设计校验，不是运行时依赖清单，
也不授权把上游仓库复制到本项目。仓库、默认分支、commit 和许可证信息于 2026-09-22
通过 GitHub API 核验；上游会继续变化，真正引入前必须重新核验许可证、版本和安全公告。

## 第一阶段参考

| 优先级 | 项目与固定 commit | 源码区域 | 借鉴内容 | 不采用部分 | 本项目落点/验证 |
| --- | --- | --- | --- | --- | --- |
| P0 | [Eino](https://github.com/cloudwego/eino/tree/b9539ec114a14c32aa568f05eda5cde1e83fcfc1)，`b9539ec114a14c32aa568f05eda5cde1e83fcfc1`，Apache-2.0 | `components/model`、`components/tool`、`compose` | 模型/工具合同、Runner、Graph/Interrupt 的执行边界 | 不把 Eino 类型带入 domain，不把 Graph DSL 作为核心状态机 | `DecisionProvider`、`WorkerRunner`；fake/适配器合同测试 |
| P0 | [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk/tree/07e46a2864f7bbc873490a7bb227977450683ab2)，`07e46a2864f7bbc873490a7bb227977450683ab2`，许可证需引入前复核 | `mcp/client.go`、`mcp/server.go`、`mcp/tool.go`、`mcp/transport.go` | Client/Server、工具发现、调用、取消和协议错误传播 | 不允许远端服务器动态扩大本地 allow-list，不把 MCP 协议泄漏到 domain | `ToolExecutor`/MCP adapter；allow-list、超时、错误分类测试 |
| P0 | [OpenTelemetry Go](https://github.com/open-telemetry/opentelemetry-go/tree/94277ba38b0d97f648cb812a55786a3fbea94da1)，`94277ba38b0d97f648cb812a55786a3fbea94da1`，Apache-2.0 | `trace/`、`metric/`、`sdk/` | API/SDK 分离、Context 传播、Provider/Exporter 生命周期 | 不让 domain 依赖 OTel，不让观测失败改变业务终态 | `TraceSink`/包装器；脱敏、关闭和 exporter 失败测试 |
| P1 | [LangGraph](https://github.com/langchain-ai/langgraph/tree/49cce0ca852be4cfb567a1cbe0e511ff325a1682)，`49cce0ca852be4cfb567a1cbe0e511ff325a1682`，MIT | `libs/checkpoint/`、checkpoint tests | 快照版本、恢复、pending writes 和 replay 语义 | 不复制其图运行时，不把 checkpoint 当业务状态 owner | `CheckpointStore`；版本连续、损坏快照和恢复合同测试 |
| P1 | [Temporal Go SDK](https://github.com/temporalio/sdk-go/tree/626130f1fd9de50cfd90b884a3fb796504f22dc1)，`626130f1fd9de50cfd90b884a3fb796504f22dc1`，MIT | `workflow/`、`activity.go`、`internal/` 的重试/取消语义 | 协作式取消、重试边界、心跳和确定性恢复 | 第一阶段不引入 Temporal Server，不宣称 exactly-once | `WorkerRunner` deadline/error contract；取消和未知结果测试 |

## 本项目基础设施锚点

以下锚点用于逐项实现和验收。它们是设计证据，不是新增运行时依赖；简单能力优先使用
Go 标准库和仓库已有依赖，不复制上游代码。

| 边界 | 锚点版本/来源 | 具体源码区域 | 借鉴规则 | 本地合同测试 |
| --- | --- | --- | --- | --- |
| Catalog/显式注册 | `suanming-agent` `850e553113232eaad6b183e63ed04b9fbb3fcd85` | `backend/internal/agentconfig/catalog.go`、`catalog_test.go`、`docs/architecture.md` | 启动加载、重复 ID/缺失引用拒绝、配置只引用 Go 已注册能力 | `internal/config/catalog_test.go`、`internal/composition/composition_test.go` |
| 配置加载 | `BurntSushi/toml` v1.4.0 + 当前配置合同 | `internal/config/config.go`、`config_test.go` | 文件 -> 环境覆盖 -> 启动校验；配置不能扩大权限 | `internal/config/config_test.go` |
| Supervisor/Runner | [Eino](https://github.com/cloudwego/eino/tree/b9539ec114a14c32aa568f05eda5cde1e83fcfc1) | `adk.Runner`、`AgentEvent`、`compose` | Context、事件和 checkpoint 是适配边界；Eino 类型不进入 domain | `internal/application/run/service_test.go`、`internal/infrastructure/eino/eino_test.go` |
| Tool/MCP | [Eino](https://github.com/cloudwego/eino/tree/b9539ec114a14c32aa568f05eda5cde1e83fcfc1)、[MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk/tree/07e46a2864f7bbc873490a7bb227977450683ab2) | Eino `components/tool`；MCP `client.go`/`tool.go` | 本地注册表拥有 allow-list；远端不能扩大权限或动态注入 Tool | `internal/infrastructure/tool/*_test.go`、`internal/infrastructure/mcp/*_test.go` |
| 内存存储/Checkpoint | Coze `fefb05ff27be1da939612fbf9faf5db62583b8ae`；Eino `compose.CheckPointStore` | `coze-studio/backend/infra/checkpoint/mem.go`；Eino `compose` checkpoint contract | 最小快照、并发保护、恢复读取不改变业务状态 owner | `internal/infrastructure/persistence/memory_test.go`、`internal/infrastructure/checkpoint/memory_test.go` |
| 文件存储 | Go 1.25 标准库；LangGraph `49cce0ca852be4cfb567a1cbe0e511ff325a1682` | `os.CreateTemp`/`os.Rename`/`encoding/json`；LangGraph checkpoint tests | 临时文件替换、版本和损坏快照错误；不宣称多主并发或 exactly-once | `internal/infrastructure/persistence/file_test.go`、`internal/infrastructure/checkpoint/file_test.go` |
| EventBus/SSE | `suanming-agent` 同一 commit；Eino `AgentEvent` | `backend/internal/sse/writer.go`；Eino runner/events reference | 事件只投影业务状态；sequence 单调、终态唯一、公开数据脱敏 | `internal/infrastructure/eventbus/*_test.go`、`internal/interfaces/http/handler_test.go` |
| 资源认证 | Coze 同一 commit | `backend/api/middleware/openapi_auth.go`；`backend/domain/permission/authz_checker.go` | 认证主体与资源授权分离；不把客户端主体字段当可信来源 | `internal/infrastructure/auth/*_test.go`、`internal/interfaces/http/handler_test.go` |
| 观测 | OpenTelemetry `94277ba38b0d97f648cb812a55786a3fbea94da1`；Eino callbacks | OTel `trace`/`metric`/`sdk`；`callbacks.AppendGlobalHandlers` | Provider/Exporter 生命周期显式关闭；观测失败不能改业务终态 | `internal/infrastructure/observability/observability_test.go` |

## 后续按需参考

| 能力 | 项目与固定 commit | 源码区域 | 适用条件 | 当前不做 |
| --- | --- | --- | --- | --- |
| 长期记忆 | [Mem0](https://github.com/mem0ai/mem0)，当前 commit 未在本轮可靠读取，Apache-2.0（仓库元数据） | `mem0/memory/`、`mem0/client/` | 需要跨会话事实提取、更新和检索 | 不把记忆写入核心 Run 状态，不在第一阶段引入；接入前必须重新固定 commit |
| 图记忆 | [Graphiti](https://github.com/getzep/graphiti/tree/16cdf7045378c8d53ae01f94e2fa60d238cb0f68)，`16cdf7045378c8d53ae01f94e2fa60d238cb0f68`，Apache-2.0 | `graphiti_core/edges.py`、`graphiti_core/nodes.py`、`graphiti_core/search/` | 需要实体关系和时间有效性查询 | 不为普通会话记忆预先引入图数据库 |
| 进程内授权 | [Casbin](https://github.com/apache/casbin/tree/524f3f2dc9baef696d748db491d49b3055d359d1)，`524f3f2dc9baef696d748db491d49b3055d359d1`，Apache-2.0 | `enforcer.go`、`model/`、`persist/` | 需要可配置 RBAC/ABAC 且仍在进程内 | 不替代 Tool 风险 Policy Gate |
| 策略评估 | [OPA](https://github.com/open-policy-agent/opa/tree/733bdf9b7ab74da06102f0d55747ec9e64337a41)，`733bdf9b7ab74da06102f0d55747ec9e64337a41`，Apache-2.0 | `ast/`、`topdown/`、`plugins/` | 策略需要独立发布、审计或跨服务复用 | 不把 Rego 作为状态机和终态规则 |
| 关系授权 | [OpenFGA](https://github.com/openfga/openfga/tree/ab557c5592670c899de35297e7aa067015f06502)，`ab557c5592670c899de35297e7aa067015f06502`，许可证需引入前复核 | `check/`、`graph/`、`model/`、`storage/` | 资源关系超过简单 owner 检查 | 不在当前单主体、单进程范围引入服务端授权系统 |

## 映射规则

每个上游项目在真正接入前必须补齐以下记录：

1. 使用的版本、许可证和安全公告检查结果；
2. 具体源码符号，而不是只有项目首页；
3. 借鉴的接口/状态/生命周期，以及明确不采用的行为；
4. 映射到本项目的应用契约和 `composition` 装配点；
5. 至少一个本地合同测试和一个失败路径测试。

本矩阵不支持反射扫描、运行时自注册、热加载或“万能组件”接口。若上游能力扩大了权限、
改变了状态所有权或绕过 `Policy Gate -> Approval -> Idempotency -> Tool -> Audit Event`，
只能作为研究材料，不能直接接入。

## DSH 说明

此前讨论中的 “DSH” 尚未绑定一个确定仓库。当前不把它写入依赖或参考 commit；确认具体
项目地址和许可证后，再按上面的五项记录规则加入矩阵。
