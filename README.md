# Eino Supervisor Template

**一个用确定性代码约束模型行为的 Go + Eino Agent 参考模板：模型只提出候选，权限、状态、审批、幂等和终态由确定性代码裁决，并配一套能证明自己会变红的验收门禁。**

模板可直接运行：`/api/chat` 走真实 HTTP/SSE 链路，评测命令用同一入口跑版本化 Case 并产出可复核报告，不需要网络、凭证、Docker 或真实模型。

---

## 项目目的

大模型输出即使 JSON 结构完全正确，也可能：越权调用未授权 Tool、绕过审批直接产生副作用、在恢复时重复执行已完成的写入、发出两个终态事件，或者让观测后端故障改变业务结果。

本模板要回答的问题是：**当模型不可信时，一个 Agent 服务应该长什么样。**

做法是把 LLM 的角色降到最低——它只能提出结构化的候选路由、参数和文本；其余全部交给确定性代码：

```text
模型：提出候选   →   Policy Gate：决定是否允许   →   Manager：推进已批准的计划
     →   Worker：完成单一有界任务   →   Tool：执行已声明能力   →   Final Guard：校验终态与公开文本
```

它同时是一套**可验证的工程样例**，而不是生产平台：每个安全边界都有合同测试或可重放的验收 Case，并且有一条反向验证脚本，用来证明这些门禁在被改坏时确实会变红。

---

## 架构特点

### 固定运行链路（唯一入口）

所有请求必须走这一条链路，业务代码不得绕行：

```mermaid
flowchart LR
    HTTP["HTTP/SSE + AuthN"] --> APP["Application<br/>+ resource AuthZ"]
    APP --> SUP["Supervisor<br/>候选路由"]
    SUP --> POLICY["Policy Gate<br/>确定性授权"]
    POLICY --> PLAN["Manager / ExecutionPlan<br/>状态唯一 owner"]
    PLAN --> WORKER["有界 Worker"]
    WORKER --> TOOL["注册 + 白名单 Tool"]
    TOOL --> GUARD["Final Guard"]
    GUARD --> EVENT["RunEvent"]
    EVENT --> SSE["SSE Projection"]
    APPROVAL["Approval"] -. "副作用前置" .-> PLAN
    IDEMPOTENCY["Idempotency<br/>run_id + step_id"] -.-> TOOL
```

各环节的职责是硬边界：

- `HTTP/SSE + AuthN` 只解析请求、验证 Bearer 凭证并建立流；`/api/*` 必须携带可信主体。
- `Application + resource AuthZ` 做用例编排；run 绑定创建主体，重放、审批、恢复、取消都先校验归属。
- `Supervisor` 只提出候选，不能授权能力、修改状态、生成幂等键或决定终态。
- `Policy Gate` 依据注册表、风险、权限和输入合同确定性地允许或拒绝。
- `Manager` 是运行状态与 `ExecutionPlan` 的唯一 owner。
- `Worker` 只完成一个已声明、有限时长和有限步骤的任务，没有最终答复权。
- `Tool` 只能在 Worker 允许集合内调用，且必须注册、校验输入、设置超时、分类错误。
- `Final Guard` 校验结构、敏感信息与终态；不合格只能转为分类失败，不能猜测替代结果。
- `SSE Projection` 只投影内部事件，不重新判断权限或业务状态。

### 依赖方向固定

```text
interfaces → application → domain
composition → application + domain + infrastructure
infrastructure → domain/application 合同
```

领域层不依赖 HTTP、Eino、数据库、MCP、具体模型或 SSE；基础设施实现只能通过领域或应用合同接入；`composition` 只做注册与装配，不承载业务判断。能力只在启动期显式注册，配置只能引用已注册 ID；重复 ID、缺失引用、allow-list 越权和降低审批要求的配置会在启动时直接失败。禁止反射扫描、`init()` 自注册和热加载。

`cmd/archcheck` 在本地门禁中强制这条方向，改坏即失败。

### 确定性代码拥有权威

- 权限、审批、幂等键、重试和终态由代码决定，不由模型输出推断。
- 写入、发送、删除等副作用必须经过 `Policy Gate → Approval → Idempotency → Tool → Audit Event`。
- 只读 Tool 同样要注册、白名单、校验输入、设置超时并分类错误。
- 模型输出必须经过结构化解析、Schema 校验和注册表检查才能进入执行。

### 每个状态只有一个 owner

会话状态归 `conversation`，执行计划与运行状态归 `agent/plan`，审批状态归 `approval`。不存在通用 `state` 容器；checkpoint 只是恢复机制，不是新的业务状态所有者。`SupervisorDecision`、`ApprovedRoute`、`ExecutionPlan`、`WorkerContract`、`ToolContract`、`RunEvent`、`Checkpoint`、`ApprovalRequest` 的语义保持稳定。

### 有界执行与可恢复

- 每次执行有最大步骤数、调用次数、重试预算、超时和 deadline。
- 取消是协作式语义（per-run context），不承诺强制终止任意外部进程。
- 恢复走 checkpoint；外部调用已发出但结果未提交时，返回 `RUN_OUTCOME_UNKNOWN` 并**禁止自动重试**，等待人工对账。
- 每个 run 只有一个公开终态事件。

### 评测与实现同源

验收 Case 不走内部函数，而是调用与线上一致的 `/api/chat`、approval、resume、cancel 路由，采集结构化证据后由四个确定性 Evaluator 判定。评测入口强制 fake 模型并写入显式 `evaluation_profile`，所以"评测通过"永远只代表本地合同路径通过，不会被读成真实模型能力。

---

## 适用场景

### 适合

- **有副作用的运营 / 客服 / 数据类 Agent**：发消息、下单、写库等动作需要审批、幂等与审计留痕。
- **需要人工审批介入的长流程**：审批等待、跨请求续跑、进程重启后恢复。
- **把"模型不可信"当设计前提的系统**：结构化输出 + 注册表校验 + Policy Gate + Final Guard。
- **需要可复核质量门禁的团队**：本地确定性评测，默认离线，不依赖在线模型或外部平台。
- **希望执行器可替换的项目**：换 Eino、换模型、换 Worker 实现时，HTTP、状态机、通用运行循环和 SSE 投影不变。
- **需要给评审者展示证据的项目**：逐 Case 报告、基线对比、失败归因和覆盖率对照表都在仓库里。

### 不适合

- 开放式自主循环、多 Agent 协作、DAG 工作流编排（当前最多两个有序步骤）。
- 画布、插件市场、多租户 SaaS 平台。
- 数据库级或跨进程 exactly-once、多主高可用。
- 用 LLM judge 或模型自评分作为合并门禁。

> 模板刻意不做平台级抽象：多租户、完整 RBAC、消息队列、长期记忆、多个 Supervisor 只有在需求、指标、ADR、合同和验证都具备时才会加入。

---

## 核心特性

| 特性 | 说明 |
| --- | --- |
| 固定运行链路 | HTTP/SSE 到事件投影的唯一路径，层次职责与依赖方向由 `cmd/archcheck` 强制 |
| Policy Gate | 注册表 + Worker allow-list + 风险/审批/幂等元数据的确定性授权 |
| 审批与恢复 | 副作用前置审批、拒绝即失败；checkpoint 恢复、断点续跑、`RUN_OUTCOME_UNKNOWN` 不自动重试 |
| 幂等 | `run_id + step_id` 幂等键，重复 resume 不产生第二次写入 |
| 取消 | 等待审批中与执行中都可取消，收敛到唯一 `canceled` 终态 |
| 身份与资源隔离 | Bearer 凭证哈希映射到 `Subject{TenantID, SubjectID}`；run 操作校验归属，不接受客户端声明的审批人 |
| Final Guard | 结构、敏感信息与公开文本合同校验，不合格转为分类失败 |
| 结构化事件与 SSE | 内部 `RunEvent` 携带稳定 `run_id`、单调 `sequence` 和结构化类型，SSE 只做投影 |
| 版本化评测集 | 人工评审的 JSON Dataset，用真实 HTTP/SSE 入口执行，产出 JSON / JSONL 报告 |
| 四个确定性 Evaluator | `business_correctness`、`architecture_boundary`、`side_effect_safety`、`stability` 独立报告 |
| 基线回归门禁 | 已批准的逐用例判定快照（含 `verdict_digest`），只报"此前通过、本次失败"的新退化 |
| 覆盖率与归因 | claim → Case 对照表（含显式豁免）；失败时产出可执行待办 |
| 门禁自证 | 故障注入脚本把受保护行为逐个改坏，断言门禁必须变红，结束时逐字节还原 |
| 可选观测 | OTLP/Langfuse 只作诊断出口，挂了不影响业务结果与本地判定 |
| 可替换存储与执行器 | 内存/文件默认，MySQL 可选；模型 provider 与 Worker 执行器由配置选择 |

---

## 目录结构

```text
.
├── cmd/
│   ├── server/            # 服务入口：配置加载、装配、优雅关闭
│   ├── eval/              # 评测 CLI：选择 Case、跑入口、写报告、基线门禁
│   └── archcheck/         # 依赖方向与分层检查（本地门禁的一部分）
├── internal/
│   ├── config/            # 配置解析与启动期校验（注册表、白名单、预算）
│   ├── composition/       # 只负责注册与装配，不承载业务判断
│   ├── interfaces/http/   # Gin 请求适配与 SSE 输出
│   ├── application/       # 用例编排；Manager 持有运行状态与 ExecutionPlan
│   │   ├── run/           # 运行、审批、恢复、取消、幂等
│   │   └── memory/        # 会话记忆适配
│   ├── domain/            # 领域合同与不变量，不依赖框架、模型或传输
│   │   ├── agent/         # SupervisorDecision / ApprovedRoute / Policy Gate
│   │   ├── approval/      # 审批合同
│   │   ├── conversation/  # 会话与消息
│   │   ├── identity/      # Subject / Tenant
│   │   ├── operation/     # WorkerContract / ExecutionPlan
│   │   └── tool/          # ToolContract 与风险分类
│   └── infrastructure/    # 通过合同接入的具体实现
│       ├── eino/          # Eino Supervisor 适配
│       ├── llm/           # fake / DeepSeek 客户端
│       ├── tool/          # Tool 注册表与执行池
│       ├── examplebusiness/ # 示例业务：fake Supervisor、运行装配与示例 Tool
│       ├── persistence/   # 内存 / 文件 / MySQL(GORM) 存储
│       ├── checkpoint/    # 恢复快照
│       ├── eventbus/      # 事件总线与唯一终态保证
│       ├── auth/          # Bearer 认证与资源授权适配
│       ├── mcp/           # 受控 MCP Tool 接入
│       └── observability/ # 日志、OTLP、Langfuse
├── prompts/               # 版本化 Prompt（Supervisor、分析与汇总、示例 SQL）
├── schemas/               # 结构化输出合同（Supervisor 决策 JSON Schema）
├── eval/                  # 评测资产与引擎
│   ├── datasets/          # 版本化 Dataset 与新增 Case 模板
│   ├── baselines/         # 已批准的基线快照
│   ├── evaluator/         # 四个确定性 Evaluator 与失败归因
│   ├── runner/            # 走真实 HTTP/SSE 入口的 Case Runner
│   ├── langfuse/          # 可选 Score 上报
│   └── mutation-gate.sh   # 门禁自证：注入故障并断言变红
├── docs/                  # 架构、技术基线、评测方法、能力盘点
├── config.example.toml    # 带注释的配置样例
└── docker-compose.yml     # 可选本地 Langfuse（仅用于观测验证）
```

---

## 快速开始

前置：Go 1.25+。默认路径不需要网络、凭证或 Docker。

### 1. 跑本地门禁（L0 + L1）

```bash
gofmt -l cmd internal eval          # 必须无输出
go vet ./...
go run ./cmd/archcheck              # 依赖方向检查
go test ./...
go test -race ./...
go build ./cmd/server/

# L1：用真实 HTTP/SSE 入口跑验收 Case
go build -o ./tmp/eval ./cmd/eval
./tmp/eval \
  -dataset ./eval/datasets/synthetic-operations-v2.json \
  -report ./tmp/eval-report.json \
  -code-version dev \
  -config ./config.example.toml
```

期望输出：`scenario=runtime-fake cases=9 passed=9 failed=0`。退出码 `0` 全部通过 / `1` 硬断言失败 / `2` setup error（不写报告）。

> 需要核对 `1` 与 `2` 的区别时要用构建出来的二进制：`go run` 会把任何非 0 退出码折叠成 `1`。

### 2. 启动服务

```bash
go run ./cmd/server/ -f ./config.example.toml
```

`config.example.toml` 默认使用 DeepSeek（API key 从环境变量读取）；加 `-fake` 可在无外部模型的条件下做离线 smoke。路由：`/healthz`、`/api/chat`、`/api/runs/:run_id/approval`、`/api/runs/:run_id/resume`、`/api/runs/:run_id/cancel`。

### 3. 基线回归门禁

只看"本次是否全绿"会漏掉"修好一个、弄坏一个"。基线是一份已批准的逐用例判定快照，随仓库版本化：

```bash
# 记录/更新基线（拒绝从有失败的运行生成）
./tmp/eval -dataset ./eval/datasets/synthetic-operations-v2.json \
  -report ./tmp/eval-report.json -config ./config.example.toml \
  -write-baseline ./eval/baselines/synthetic-operations-v2.json

# 相对基线跑门禁：出现 regression / missing 退出码 1，版本不一致退出码 2
./tmp/eval -dataset ./eval/datasets/synthetic-operations-v2.json \
  -report ./tmp/eval-report.json -config ./config.example.toml \
  -baseline ./eval/baselines/synthetic-operations-v2.json
```

`still_failing`（基线里本来就失败的用例）不算新退化；`new`（本次新增）只提示不失败。基线必须在**完整数据集**上比较，不要与 `-risk` / `-impact-tags` 的窄选集混用，否则未被选中的用例会全部报成 `missing`。

### 4. 证明门禁不是摆设

```bash
./eval/mutation-gate.sh
```

脚本会依次把 6 处受保护行为改坏（白名单越权、跳过审批、放行敏感输出、双终态、取消失效、幂等去重失效），每次跑门禁并断言必须变红，然后从备份逐字节还原源码。**只要有一条没被抓到，脚本以非 0 退出并指名该行**——那说明这条保护缺少覆盖，是下一步要补的测试或验收 Case。

---

## 评测体系

### 分层门禁

| 层 | 内容 | 是否可阻断 |
| --- | --- | --- |
| **L0** | 格式、静态检查、依赖方向、单元与合同测试（含 race） | 是 |
| **L1** | 真实 HTTP/SSE 链路上的确定性验收 Case、覆盖率校验、基线回归 | 是 |
| **L2** | 可选 LLM judge 与在线模型评测 | 否，只作补充证据 |

只有确定性层有权阻断合并。上表的命令可以原样搬进任意 CI；托管平台的 Required Checks 需要单独配置，不能只凭存在一个 workflow 文件就宣称合并门禁已生效。

### 闭环：需求 → 用例 → 报告 → 待办 → 补用例

```bash
# 每条 claim 要么指向 Case，要么显式豁免并写明理由
go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v2.json \
  -check-coverage ./eval/coverage.json -config ./config.example.toml

# 失败时额外产出可执行待办（人读 + 机器读）
go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v2.json \
  -report ./tmp/eval-report.json -triage ./tmp/eval-triage.json \
  -config ./config.example.toml
```

修完必须补一个能抓住该回归的 Case，并用 `./eval/mutation-gate.sh` 证明它确实会红。

### 报告与场景标签

报告（JSON 与 JSONL）带 `report_schema_version`、Dataset/Case 版本、`code_version`、逐 Case 的 `run_id`、HTTP 状态、事件顺序、trace ID、终态、耗时、重试、fake write count、cleanup 结果、四个 Evaluator 的独立结论，以及失败时的 `failed_assertion`、failure taxonomy、evidence reference 和可空 `human_disposition`。不写凭证、完整 Prompt、SQL 参数或未脱敏 Tool payload。

报告还带显式 `evaluation_profile`，区分 `runtime-fake`、`runtime-real`、`coding-fake`、`coding-real`。当前只有 `runtime-fake` 有可执行入口：`cmd/eval` 即使环境变量选了真实模型也会强制 fake，因此它的结论只代表本地合同路径，不能描述 DeepSeek 等真实模型的能力；其余场景是合同边界，标为 deferred。

### 新增一条 Case

从 [`eval/datasets/feature-acceptance-template.json`](./eval/datasets/feature-acceptance-template.json) 复制：它包含一个可被 Loader 加载、并在本地 fake 链路通过的只读 Case。替换 id、目标、请求文本、期望结果和 `forbidden_side_effects` 即可；需要验收"JSON 输出 + 最终状态 + 运行证据"时，可追加数据库与日志后置断言（只保留行数、字段摘要哈希和脱敏 phase）。

---

## 配置

启动时配置按以下顺序生效：

```text
config.example.toml（或 -f 指定的 TOML）
  → TOML 同目录 .env / 已声明环境变量覆盖
  → cmd/server -fake 最后强制 provider=fake、model=fake-model
```

| 环境变量 | 覆盖字段或作用 |
| --- | --- |
| `MODEL_PROVIDER` | `[model].provider`（`deepseek` / `fake`） |
| `LLM_MODEL` / `LLM_BASE_URL` | `[model].name` / `[model].base_url` |
| `FAKE_MODEL` | 为 `true` 时把 provider/name 切到 `fake` / `fake-model` |
| `LISTEN_ADDR` / `LOG_LEVEL` | `[server].listen_addr` / `[observability].log_level` |
| `PERSISTENCE_BACKEND` / `PERSISTENCE_DIR` | `[storage].backend` / `[storage].dir` |
| `MYSQL_ENABLED` / `MYSQL_DSN_ENV` | `[mysql].enabled` / `[mysql].dsn_env` |
| `OTEL_ENABLED` / `OTEL_EXPORTER_OTLP_*` | OTLP 导出开关、endpoint、insecure、service name、请求头 |
| `LANGFUSE_ENABLED` / `LANGFUSE_ENDPOINT` / `LANGFUSE_HEADERS_ENV` | Langfuse 观测接入 |

配置只能选择**启动时已注册**且获准的实现，不能新增 Worker/Tool、扩大权限、降低审批要求或绕过 Policy Gate。密钥只从服务端配置或凭证系统解析，禁止进入 Prompt、事件正文和普通日志。

---

## 技术栈

| 组件 | 版本 / 说明 |
| --- | --- |
| Go | 1.25 |
| Eino | `github.com/cloudwego/eino` v0.9.12（Supervisor Graph 适配） |
| 模型 | DeepSeek（`eino-ext` model 组件）/ 本地 fake |
| HTTP | Gin v1.12，SSE 单向投影 |
| 存储 | 内存 / 文件；可选 MySQL + GORM v1.31 |
| 观测 | OpenTelemetry v1.43（OTLP trace/metric），可选 Langfuse |
| 评测 | 纯标准库 CLI + 版本化 JSON 资产，无第三方评测框架 |

---

## 项目状态与边界

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| 固定运行链路、Policy Gate、审批、幂等、取消、恢复 | `implemented` | 有合同测试与可重放验收 Case |
| 版本化 Dataset、HTTP/SSE Runner、JSON/JSONL 报告 | `implemented` | 本地命令可实际跑通，默认不依赖外部服务 |
| 四个确定性 Evaluator、基线门禁、覆盖率与归因、改坏验证 | `implemented` | 均为本地离线门禁 |
| 失败 taxonomy、人工 disposition、Judge 合同 | `partial` | 合同与脱敏摘要已有；Judge 仍不进入门禁 |
| OpenTelemetry / Langfuse 观测出口 | `partial` | 有低基数关联与运行证据；在线 Dataset/Score 同步 deferred |
| MySQL / GORM | `partial` | 有 adapter 与本地 smoke；不是默认依赖，不代表生产 HA |
| 真实模型驱动的 Eino 多步 ReAct / Graph Runner | `deferred` | 当前默认 Runner 是最小单 Tool 适配器 |
| 在线 LLM Judge、`coding-*` 场景 | `deferred` | 不进入默认硬门禁 |
| 真实 CRM / 营销触达 | `deferred` | 只有合成 fixture 和 fake Tool |
| 并行 / DAG / 开放式自主循环 | `deferred` | 当前最多两个有序步骤 |
| 跨进程 exactly-once、多租户平台 | `deferred` | 文件快照不提供分布式 exactly-once |

`implemented` 只表示当前代码和本地验证达到该范围；不要把计划文档的勾选、配置字段存在或 exporter 初始化当作实现证据。当前能力盘点见 [docs/current-capability-and-gap-report.md](./docs/current-capability-and-gap-report.md)，逐项进度与验证证据见 [PROGRESS.md](./PROGRESS.md)。

---

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/architecture.md](./docs/architecture.md) | 目录职责、依赖方向与运行链路的事实来源 |
| [docs/evaluation-method.md](./docs/evaluation-method.md) | 分层门禁、报告字段、基线、覆盖率、改坏验证 |
| [docs/technology-baseline.md](./docs/technology-baseline.md) | 技术选型基线与宪法约束关系 |
| [docs/component-reference-map.md](./docs/component-reference-map.md) | 参考项目能力对比与状态标签 |
| [docs/current-capability-and-gap-report.md](./docs/current-capability-and-gap-report.md) | 当前能力盘点与差距 |
| [PROGRESS.md](./PROGRESS.md) | 进度与验证状态快照（只记录实际执行过的证据） |

---

## 许可证

本项目原创代码和文档采用 [MIT License](./LICENSE)。第三方依赖按各自许可证使用。
