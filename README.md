# Eino Supervisor Template

一个用 Go 和 Eino 搭建的有界 Agent 服务模板。模型只负责提出候选路由；认证、权限、审批、幂等、运行状态和最终事件由应用代码决定。

这个仓库的重点是把一条可检查的运行链路跑通：HTTP/SSE 请求经过应用层、Policy Gate、执行计划、Worker 和 Tool，最后变成有序的运行事件。评测命令走同一个入口执行版本化 Case 并产出可复核报告，本地门禁不需要网络、凭证、Docker 或真实模型。示例业务是合成的，不连接真实 CRM、营销渠道或客户数据。

## 快速开始

需要 Go 1.25 或更高版本。依赖下载完成后，本地 fake 和评测不需要模型密钥、Docker 或外部服务。

### 跑本地检查（L0）

```bash
gofmt -l cmd internal eval          # 必须无输出
go vet ./cmd/... ./internal/... ./eval/...
go run ./cmd/archcheck              # 依赖方向 + 领域包隔离 + 配置字段消费方
go test ./cmd/... ./internal/... ./eval/...
go test -race ./cmd/... ./internal/... ./eval/...
go build ./cmd/server/
```

### 跑验收用例（L1）

运行版本化的 HTTP/SSE 验收用例：

```bash
mkdir -p tmp
go build -o ./tmp/eval ./cmd/eval
./tmp/eval \
  -dataset ./eval/datasets/synthetic-operations-v5.json \
  -report ./tmp/eval-report.json \
  -code-version dev \
  -config ./config.example.toml
```

当前数据集 `synthetic-operations@5` 包含 13 个 Case。全部通过时输出 `scenario=runtime-fake cases=13 passed=13 failed=0`，退出码为 `0`；断言失败为 `1`；数据集、覆盖率或装配无效为 `2`。需要区分 `1` 和 `2` 时使用构建出的二进制，因为 `go run` 会把非零退出码折叠成 `1`。

要验证这些门禁确实会变红，运行 `./eval/mutation-gate.sh`（见「门禁自证」）；它会临时改坏源码，必须独占工作区。

### 基线回归门禁

只看"本次是否全绿"会漏掉"修好一个、弄坏一个"。基线是一份已批准的逐用例判定快照，随仓库版本化：

```bash
# 记录/更新基线（拒绝从有失败的运行生成）
./tmp/eval -dataset ./eval/datasets/synthetic-operations-v5.json \
  -report ./tmp/eval-report.json -config ./config.example.toml \
  -write-baseline ./eval/baselines/synthetic-operations-v5.json

# 相对基线跑门禁：出现 regression / missing 退出码 1，版本不一致退出码 2
./tmp/eval -dataset ./eval/datasets/synthetic-operations-v5.json \
  -report ./tmp/eval-report.json -config ./config.example.toml \
  -baseline ./eval/baselines/synthetic-operations-v5.json
```

`still_failing`（基线里本来就失败的）不算新退化，`new`（本次新增）只提示不失败。基线必须在**完整数据集**上比较，不要与 `-risk` / `-impact-tags` 的窄选集混用，否则未被选中的用例会全部报成 `missing`。

### 加一条验收 Case

从 [`eval/datasets/feature-acceptance-template.json`](./eval/datasets/feature-acceptance-template.json) 复制：它包含一个可被 Loader 加载、并在本地 fake 链路通过的只读 Case。替换 id、目标、请求文本、期望结果和 `forbidden_side_effects` 即可；需要验收"JSON 输出 + 最终状态 + 运行证据"时，可追加数据库与日志后置断言（只保留行数、字段摘要哈希和脱敏 phase）。新增的 claim 要同步登记进 `eval/coverage.json`，否则覆盖率校验会失败。

### 启动服务

```bash
cp .env.example .env
go run ./cmd/server/ -f ./config.example.toml -fake
```

`-fake` 使用本地 fake 模型，不需要模型密钥；API 仍需要 Bearer 凭证。示例凭证是 `demo-token`：

```bash
curl -N -X POST http://127.0.0.1:8080/api/chat \
  -H 'Authorization: Bearer demo-token' \
  -H 'Content-Type: application/json' \
  -d '{"conversation_id":"c1","message":"查询最近30天活跃客群"}'
```

默认配置的模型 provider 是 DeepSeek。去掉 `-fake` 后，API key 从 `[model].api_key_env` 指定的环境变量读取。不要把原始 token 或 API key 写进配置文件。

服务路由：

- `GET /healthz`：返回 JSON `{"status":"ok"}`（200）；依赖不完整时 `{"status":"unhealthy"}`（503）。无需凭证。
- `POST /api/chat`
- `POST /api/runs/:run_id/approval`
- `POST /api/runs/:run_id/resume`
- `POST /api/runs/:run_id/cancel`

除 `/healthz` 外，所有接口都要求 `Authorization: Bearer <token>`。

## 这个模板解决什么问题

大模型输出即使 JSON 结构完全正确，也可能做出这些事：越权调用未授权 Tool、绕过审批直接产生副作用、恢复时重复执行已完成的写入、发出两个终态事件，或者把不合格的内容当成最终答复。

**本模板要回答的问题是：当模型不可信时，一个 Agent 服务应该长什么样。** 做法是把 LLM 的权限降到最低——它只能提出结构化的候选路由、参数和文本，其余交给确定性代码。它是一套可验证的工程脚手架，不是生产平台：每个安全边界都有合同测试或可重放的验收 Case，并且有一条反向验证脚本，证明这些门禁在被改坏时确实会变红。

| | |
| --- | --- |
| **适合** | 有副作用的运营 / 客服 / 数据类 Agent（发消息、下单、写库需要审批、幂等与审计留痕）；需要人工审批介入的长流程；把"模型不可信"作为设计前提的系统；需要可复核质量门禁的团队 |
| **不适合** | 开放式自主循环、多 Agent 协作、DAG 工作流编排；画布、插件市场、多租户 SaaS；数据库级或跨进程 exactly-once、多主高可用；用 LLM judge 或模型自评分作为合并门禁 |

## 运行边界

请求按下面的顺序处理：

```text
HTTP/SSE + AuthN
  -> Application + resource AuthZ
  -> Supervisor  （只提结构化候选）
  -> Policy Gate  （确定性授权）
  -> Manager / ExecutionPlan  （状态唯一 owner）
  -> bounded Worker
  -> allow-listed Tool        （副作用前经 Approval，幂等键去重）
  -> Final Guard
  -> RunEvent                 （每个 run 只有一个公开终态）
  -> SSE projection
```

- Supervisor 和模型只能提交结构化候选，不能授权 Tool、修改运行状态或决定终态。
- Policy Gate 只允许启动时注册、且在 Worker allow-list 中的能力。
- Manager 持有 `ExecutionPlan` 和运行状态；Worker 没有最终答复权。
- 有副作用的 Tool 必须先获得动作级审批，再使用幂等键执行。重复 resume 不会重复写入。
- `/api/*` 先认证主体，再检查 run 的归属；客户端提交的 `reviewer` 不作为可信身份。
- 取消通过 `context.Context` 协作完成，不承诺强制终止不响应取消的外部进程。
- 每个 run 只产生一个公开终态事件。外部调用已经开始但结果未确认时，运行会进入未知结果状态，不自动重试。

这里的 supervisor 是**应用层的确定性 supervisor**：`internal/infrastructure/llm/` 把模型输出变成结构化候选（`SupervisorDecision`），授权、计划与终态由 `internal/domain/agent/` 和 `internal/application/run/` 决定。Eino 侧用的是官方推荐的 `adk.ChatModelAgent` + `adk.Runner` + `compose.CheckPointStore`；`adk/prebuilt/supervisor` 未使用（Eino 官方在源码里标注它 NOT RECOMMENDED，建议改用 ChatModelAgent + AgentTool 或 DeepAgent）。

**有界执行与可恢复**：每次执行有最大步骤数、调用次数、重试预算、超时和 deadline；取消是协作式语义（per-run `context.Context`）；恢复走 checkpoint，外部调用已发出但结果未提交时返回 `RUN_OUTCOME_UNKNOWN` 并**禁止自动重试**，等待人工对账。

事件会被保存后按顺序投影到 SSE；当前实现提供心跳和有序重放，不应把它理解为实时 token 流。

## 架构防腐

分层架构烂掉通常不是因为没人写文档，而是因为**没有任何检查会因为被破坏而变红**：`cmd/archcheck` 在 CI 里执行三条互相不能替代的规则，任一条被违反即以非 0 退出。

| 规则 | 挡住的退化 |
| --- | --- |
| **依赖方向** | `domain` 不得依赖 application/interfaces/infrastructure；`application` 不得依赖 interfaces/infrastructure；`interfaces` 不得依赖 infrastructure（`observability` 合同是唯一的传输侧例外）；`infrastructure` 不得依赖 `internal/config`；`cmd/*` 不得依赖 application |
| **领域包只许标准库与 `internal/domain/**`** | 上一条只比较**模块内**路径前缀，对模块外 import 一律放行——领域包引入 Eino、GORM 这类第三方会**静默通过**，这条规则单独堵它 |
| **每个 TOML 配置字段必须有消费方** | "声明了却没接线"的配置项，恰恰是最容易让人误以为某项能力已经生效的地方 |

与之配套的全局约束：

- **依赖方向固定**：`interfaces -> application -> domain`；`infrastructure` 通过领域或应用合同接入；`composition` 只做注册与装配，不承载业务判断。
- **能力只能显式注册**：Worker、Route、Prompt、Runner、Tool、模型 provider 都在启动期冻结，配置只能引用已注册 ID。重复 ID、缺失引用、allow-list 越权、降低审批要求、未注册 provider 都会让启动直接失败；禁止反射扫描、`init()` 自注册和热加载。
- **每个状态只有一个 owner**：会话归 `conversation`，执行计划与运行状态归 `agent`，审批归 `approval`；checkpoint 只是恢复机制，不是新的状态所有者。

## 评测

Case 走的是与线上完全一致的 `/api/chat`、approval、resume、cancel 路由，判定只读结构化证据，报告显式写明它到底跑了哪个执行器。可阻断合并的只有 L0 与 L1：

| 层 | 内容 | 是否阻断 |
| --- | --- | --- |
| **L0** | 格式、静态检查、依赖方向（`cmd/archcheck`）、单元与合同测试（含 `-race`） | 是 |
| **L1** | 真实 HTTP/SSE 链路上的确定性验收 Case、claim ↔ Case 覆盖率校验、基线回归 | 是 |
| **L2** | 可选 LLM judge 与分数上报 | 否。当前没有可执行的 judge，只有 `-langfuse-upload` 这个非阻断的分数 sink |
| **L3** | 抽样人工评审与 judge 校准 | 否 |

L2 不能把 L1 的红改判成绿：权限、审批、幂等、终态和副作用计数只由确定性代码决定。

四个确定性 Evaluator 各自硬断言一个维度：

| Evaluator | 硬断言什么 |
| --- | --- |
| `business_correctness` | 终态、事件类型序列、Case 声明的结构化结果字段 |
| `architecture_boundary` | 每次 Tool 调用都能对上启动期注册与 Policy 证据 |
| `side_effect_safety` | 副作用的前置审批与幂等、写入计数、cleanup |
| `stability` | 事件序号单调、trace 关联、耗时与重试预算、重复判定稳定 |

证据合同本身也在判定范围内：case/run 标识必须与本次 Case 一致，EventID 必须唯一且归属同一个 run，序号必须单调，终态事件只能有一个；缺少必需的治理证据、出现匿名 side-effect 调用或审批被借用到别的动作上，都是**硬失败**。声明了却采集不到的证据不会因为"某个 evaluator 恰好没看到对应事件"就被动通过。

报告（JSON 与 JSONL，权限 `0600`）逐 Case 记录 `run_id`、HTTP 状态、事件顺序、trace ID、终态、耗时、重试、fake write count、cleanup 结果、四个 Evaluator 的独立结论，以及失败时的 `failed_assertion`、failure taxonomy、evidence reference 和可空 `human_disposition`；不写凭证、完整 Prompt、SQL 参数或未脱敏 Tool payload。报告带显式 `evaluation_profile`：当前只有 `runtime-fake` 有可执行入口（`cmd/eval` 即使环境变量选了真实模型也会强制 fake），所以它的结论不能描述 DeepSeek 等真实模型的能力。

### 评测闭环

单次运行是单向的（跑 → 报告 → 结束），闭环靠三件事合上：

```bash
# 每条 claim 要么指向 Case，要么显式豁免并写明理由
# 当前 claims=14 covered=11 waived=3 cases_without_claim=0
go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v5.json \
  -check-coverage ./eval/coverage.json -config ./config.example.toml

# 失败时额外产出可执行待办（人读 + 机器读）
go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v5.json \
  -report ./tmp/eval-report.json -triage ./tmp/eval-triage.json -config ./config.example.toml
```

每一环都会失败：`eval/coverage.json` 是人工评审过的 claim → Case 对照表，指向不存在的用例、既豁免又映射、或既不映射又不豁免，都以退出码 `2` 失败，**没有用例支撑的 claim 藏不住**；`-triage` 把每条失败断言变成待办（哪个 Case、哪条断言、哪类失败、先看哪里；未知类别交人工判断而不是丢弃）；修完必须补一个能抓住该回归的 Case，并用 `./eval/mutation-gate.sh` 证明它确实会红。基线快照记录的不只是通过与否，还有每个 Case 的 `verdict_digest`（不含按请求生成的 trace id，同一份代码独立跑两次得到相同摘要），重新批准时比对的是整份"测量身份"，标注相同而证据变了会被暴露出来。

### 门禁自证

一个不会变红的门禁等于没有门禁。`eval/mutation-gate.sh` 对 7 处受保护行为注入**真实故障**——直接改源码 → 跑门禁 → 断言必须变红 → 立即从备份逐字节还原。这 7 处是：Worker/Tool 白名单、副作用前置审批、敏感输出拦截、终态唯一性、取消信号、幂等键去重、审批绑定到即将执行的那个动作。

**有一条没被抓到，或者有注入因锚点失配而没能施加，脚本都会指名该行并以非 0 退出**：退出码 `1` 说明这条保护缺少覆盖，是下一步要补的测试或验收 Case；退出码 `2` 说明注入表已经和源码脱节（锚点失配、注入后编译不过），或者探针在干净树上已经变红——那次改坏验证本身不带信息。两者同时出现报 `2`。哪一种都不是可以忽略的噪音。

输出是一张 `mutation × L0 × L1` 矩阵，两列都要读：`L1` 是验收数据集，`L0` 是 Go 合同测试。白名单、幂等去重、终态唯一性三行只有 `L0` 变红，它们在 `L1` **结构性不可达**（原因与代码证据写在 `eval/coverage.json` 的同名豁免 claim 里），其余四行两层都能抓住。脚本会先在不注入的干净树上跑一遍探针：任一层在干净树上就是红的，它立即以 `2` 中止且不施加任何注入。

CI（[`.github/workflows/ci.yml`](./.github/workflows/ci.yml)）分成 `deterministic` 与 `mutation` 两个 job 执行上述 L0/L1 与门禁自证；两个 job 需要在仓库设置里被设为 Required Check 才真正阻断合并——这是平台配置，不是代码。

## 配置

配置文件是 [`config.example.toml`](./config.example.toml)。生效顺序为：

1. `-f` 指定的 TOML；
2. TOML 同目录的 `.env`；
3. 已存在的进程环境变量覆盖前两项；
4. `-fake` 最后把 provider 固定为 `fake`、模型固定为 `fake-model`。

配置只能选择已注册的 provider、Worker、Runner 和 Tool，不能通过配置新增能力、扩大 allow-list 或降低审批要求。相对路径资源（例如 `prompts/`）按配置文件所在目录解析。

常用环境变量包括 `MODEL_PROVIDER`、`LLM_MODEL`、`LLM_BASE_URL`、`FAKE_MODEL`、`LISTEN_ADDR`、`PERSISTENCE_BACKEND`、`MYSQL_ENABLED` 和 `OTEL_*`。完整字段和默认值以 `config.example.toml` 为准。

## 扩展示例业务

模板自带的示例业务集中在 [`internal/infrastructure/examplebusiness`](./internal/infrastructure/examplebusiness)。新增一条业务链路的真实改动面是：

1. `registry.go` 登记新链路：常量、`AllowedTools`、`routes`、`ToolContracts`、`ToolIDs`、`ToolImplementations` switch 共 6 处；漏登会让 `TestContractsAreExplicitAndStable` 失败，这是设计意图，不是误报。
2. `fake_runtime.go` 与 `fake_supervisor.go` 补确定性输入，让本地 fake 链路能走到这条链路。
3. `toolhandlers.go` 注册 Tool handler；本包只依赖"注册表/执行器"这类能力接口，不 import Tool 实现包。
4. `config.example.toml` 三处登记：worker allow-list、route、tools——未登记的引用会让启动直接失败。
5. `prompts/` 放该 Worker 的模型指令（用 `eino_adk` runner 时必需）。
6. `eval/datasets/` 与 `eval/coverage.json` 补验收 Case 与 claim，否则覆盖率校验会失败。

`internal/composition/` 不需要为单个 Tool 或 Worker 改动——它只调用整包入口（`Workers()`、`Routes()`、`ToolContracts()`、`RegisterToolHandlers()`）。替换整个示例业务时，替换 `examplebusiness` 包并改这些调用点即可。新增能力仍要经过注册表、Policy Gate 和对应测试。

## 项目结构

| 路径 | 职责 |
| --- | --- |
| `cmd/server/` | 加载配置并启动 HTTP 服务 |
| `cmd/eval/` | 运行验收 Case、生成报告和基线结果 |
| `cmd/archcheck/` | 依赖方向、领域包隔离、配置字段消费方三条防腐规则 |
| `internal/config/` | 配置解析、环境变量覆盖和启动校验 |
| `internal/domain/` | 领域合同和状态不变量（不依赖 HTTP、Eino、数据库、MCP、模型或 SSE） |
| `internal/application/` | 用例编排、运行状态和执行计划 |
| `internal/interfaces/` | HTTP/SSE 适配 |
| `internal/infrastructure/` | Eino、模型、Tool、存储、认证和观测适配器 |
| `internal/composition/` | 注册、实现选择和依赖装配（不承载业务判断） |
| `eval/` | Dataset、Runner、Evaluator、基线、claim 覆盖率表和 mutation gate |
| `prompts/`、`schemas/` | 版本化 prompt 和结构合同 |

合并门禁的实际入口是 [`.github/workflows/ci.yml`](./.github/workflows/ci.yml)；配置字段与环境变量以 `config.example.toml` 为准。

## 当前状态

已实现并有本地验证：固定运行链路、Policy Gate、动作级审批、幂等、取消、文件/内存存储、确定性评测、基线回归和故障注入门禁。

部分实现：MySQL/GORM 只承载可选订单 Tool 与租约，不代表生产高可用；Langfuse 只是非阻断的分数 sink（缺凭证只 warning，不改变判定）。

仍是明确延期：实时 SSE token 流、完整 OIDC/JWT/RBAC、多租户、跨进程 exactly-once、生产级 MySQL 状态后端、在线 LLM Judge、开放式 ReAct/Graph/DAG，以及真实 CRM 或营销触达。这里的 `fake` Tool 只用于合同验证，不代表外部业务已接入。

## 许可证

本项目原创代码和文档采用 [MIT License](./LICENSE)；第三方依赖按各自许可证使用。
