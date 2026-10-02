# 本地评测方法

本项目的评测事实来源是版本化 JSON Dataset、真实本地 HTTP/SSE Runner 和确定性报告。Runner 只调用现有 `/api/chat`、approval、resume、cancel 路由；它不直接拥有运行状态、Policy、审批、幂等或终态。

## 配置优先级与执行边界

启动配置按 `TOML -> 同目录 .env / 已声明环境变量 -> cmd/server -fake 最后强制 fake` 生效。`.env` 自动加载是 `cmd/server` 的入口行为；`cmd/eval` 使用进程已有环境变量并在本地 Runner 中再次强制 fake。`config.example.toml` 默认 provider 是 `deepseek`，所以 `cmd/server` 不带 `-fake` 时可能调用 DeepSeek；`MODEL_PROVIDER`、`LLM_MODEL`、`LLM_BASE_URL`、`FAKE_MODEL`、`LISTEN_ADDR`、`LOG_LEVEL`、`PERSISTENCE_BACKEND`、`PERSISTENCE_DIR`、`MYSQL_ENABLED`、`MYSQL_DSN_ENV` 以及 `OTEL_ENABLED`、`OTEL_EXPORTER_OTLP_ENDPOINT`、`OTEL_EXPORTER_OTLP_INSECURE`、`OTEL_SERVICE_NAME`、`OTEL_EXPORTER_OTLP_HEADERS`、`LANGFUSE_ENABLED`、`LANGFUSE_ENDPOINT`、`LANGFUSE_HEADERS_ENV` 是约定的覆盖入口。`FAKE_MODEL=true` 会把 provider/name 固定为本地 `fake`/`fake-model`；它不是任何真实模型名称。

`cmd/eval` 的本地 Runtime Runner 在创建测试服务后强制 `provider=fake`、`model=fake-supervisor@1`，即使环境中有 `MODEL_PROVIDER=deepseek`；因此该命令不会调用真实模型。fake 结果只能证明本地合同和执行路径，不能作为 DeepSeek/GPT/Qwen 的能力统计。

## Case 清单

`eval/datasets/synthetic-operations-v2.json` 当前包含九个固定 Case，覆盖四类：

- `positive-audience-query`：只读客群查询。
- `positive-serial-summary`：两步串行查询和汇总。
- `diversity-audience-subject-variant`：变更匿名主体和查询表达的多样性 Case。
- `boundary-approved-outreach`：审批后模拟触达、重复 resume。
- `negative-reject-outreach`：拒绝副作用并保持无写入。
- `negative-unknown-capability`：未知能力被 Policy Gate 拒绝。
- `negative-invalid-input`：空消息在 HTTP 边界被拒绝。
- `boundary-cancel-outreach`：等待审批时取消且不写入。
- `boundary-cancel-while-executing`：Run 仍在执行时取消：证明取消信号真的接在执行路径上，而不是只在等待审批时才生效。

Case 的 `id`、版本、风险、影响标签、步骤、终态、事件顺序、禁止副作用、timeout、retry budget、evidence requirements 和 evaluator 规则均由人工 JSON 定义。未知字段、重复 key/ID、非法 timeout/retry、未知 fixture/evidence/evaluator 会在加载阶段拒绝。每个可运行 Dataset Case 必须声明四个硬评测维度：`business_correctness`、`architecture_boundary`、`side_effect_safety`、`stability`；Loader 还会按 Case 类型强制注入并校验最小证据矩阵（正常 Run 需要 run state、HTTP status、trace、cleanup、事件序号；预期 Tool/审批路径分别需要 Tool call/approval evidence）。运行时固定执行四个硬维度，Case 声明只作为不可关闭的合同校验；声明但未采集到的证据会产生硬失败，不能因为某个 evaluator 没有看到对应事件而被动通过。

## 报告字段

报告包含 Dataset/Case version、选择的风险与影响标签、`code_version`、生成时间、每个 `run_id`、HTTP 状态、事件顺序、trace ID、终态、耗时、重试计数、fake write count、cleanup result 和四个 evaluator 的独立结果。失败结果保留结构化 `failed_assertion`、failure taxonomy、evidence reference 和可空 human disposition；不写凭证、完整 Prompt、SQL 参数或未脱敏 Tool payload。文件权限为 `0600`。`business_correctness` 硬断言终态、事件顺序及 Case 声明的结构化结果字段，报告仅保留预期字段投影。

报告还必须包含入口生成的 `evaluation_profile`：`evaluation_plane`、`scenario`、`model_provider`、`model_name`、`executor`、`verifier`、`network_mode` 和 `tier`。四种场景是 `runtime-fake`（`/api/chat` + fake Supervisor）、`runtime-real`（`/api/chat` + 真实模型）、`coding-fake`（FakeExecutor + FakeVerifier）和 `coding-real`（真实 Coding Agent + 隔离执行器 + 独立 Verifier）。四种里只有 `runtime-fake` 有可执行入口：其余三种是 profile/合同边界，仓库内没有任何命令能产出它们的报告，未实现的场景必须标为 deferred，不能用 Runtime 结果冒充 coding 结果。Profile 不根据模型输出推断；场景不匹配时返回 `evaluation_setup_error`，停止评分。

PR 默认只运行 `runtime-fake`。`cmd/eval` 即使环境变量选择 DeepSeek 也强制 fake，因此 fake 报告不能被描述为真实模型能力。本地 Runtime Runner 只接受 `runtime-fake` + `local-fake` + `fake` 的 profile；入口在运行 Case 和写报告之前先校验，不匹配时以退出码 `2` 返回 `evaluation_setup_error`，不留下报告文件。`runtime-real` 报告只能来自 `cmd/eval` 之外的一次显式真实模型运行，必须单独记录，不能与 fake 报告合并。

### 报告 schema 版本

报告（JSON 与 JSONL envelope）都带 `report_schema_version`，当前为 `2`。语义：**消费方看不懂的版本必须拒绝处理，而不是按新语义误读**。基线比较会校验该版本：低于支持下限、或与基线记录不一致，都返回 `evaluation_setup_error` 且退出码 `2`。新增字段属于向后兼容的演进，但改变既有字段含义时必须递增该版本。

### fixture 引用的语义

Case 的 `preconditions.fixture` 是一个**名字**，用于标识该 Case 期望的环境并作为报告中的关联元数据；**实际被测环境由 `cmd/eval -config` 指向的配置决定**。加载与运行时只校验名字格式（`[a-z][a-z0-9_]*`），格式非法返回 `evaluation_setup_error`。

这里刻意**不**维护"已注册环境"的白名单：历史上评测引擎把环境名写死成单个值，导致换环境必须改引擎源码；而引入注册表的代价（一个接口 + 一个注册表）在只有一个空实现时并不划算。等真的需要第二个环境时，再连同它的准备与清理需求一起设计。

## Case authoring

针对一次功能变更，Skill 先提出 1~3 个 Case，人工确认后再写入版本化 Dataset。每个 Case 至少说明：请求步骤、期望 JSON/状态/事件、禁止副作用、证据来源、timeout/retry budget 和 cleanup policy。验收证据优先使用可观察事实：HTTP 状态与 JSON 字段、SSE 事件顺序和终态、Policy/审批/幂等事件、数据库最终状态摘要、脱敏结构化日志。生成的 Case 是候选合同，不是实现正确性的自证。

新 Case 从 `eval/datasets/feature-acceptance-template.json` 复制：它包含一个已通过 `eval.Load` 且在本地 fake 链路上通过的只读 Case，复制后替换 id、目标、请求文本、期望结果和 `forbidden_side_effects` 即可。Loader 会拒绝未知字段、重复 id/version、非正 timeout、负 retry budget、非 `isolated_run` 的 cleanup policy 和未知 category；`go test ./eval/` 中的 `TestLoadShippedFeatureAcceptanceTemplate` 保证模板本身始终可加载。

四个硬评测维度是 business correctness、architecture boundary、side-effect safety 和 stability。每个 Case 还经过统一 evidence integrity 校验：Evidence 必须关联非空且匹配的 Case/版本/Run，事件必须有非空且唯一的 EventID，Event RunID/trace 必须与 Evidence 关联，sequence 从 1 连续递增；`Terminal` 必须对应唯一 terminal event；Policy/Plan 证据必须发生在 `tool_call` 之前。architecture 使用启动快照中的 Worker allow-list 与 Tool 风险/审批/幂等元数据；side-effect 判断依据合同风险，不依赖某个具体 Tool ID。审批必须精确匹配 `step_id + worker_id + tool_id`，fake write 必须有 side-effect Tool 归因，只读 Tool 不能借用写入证据，同一绑定键重复 side-effect `tool_call` 会硬失败。证据合同还会检查 Tool 调用存在、Policy evidence、事件序号单调、终态唯一、trace、HTTP 状态和 cleanup。退出码 `0` 表示所有 Case 通过，`1` 表示运行或硬断言失败，`2` 表示 Dataset/配置/装配失败。

## 执行中取消与慢步骤

`chat_cancel` 动作在 Run **仍在执行**时发出取消，用来证明 cancel signal 真的接线：
只取消"等待审批中"的 Run 走不到执行窗口，那条路径无法发现取消信号失效。

```json
{"action": "chat_cancel", "message": "分析沉睡客户",
 "conversation_id": "eval-cancel-executing", "cancel_after_ms": 150}
```

触发方式是**定时器而不是 SSE 事件**：HTTP 层在 Run 结束后才一次性写出事件流
（`handler.go` 在 `Start` 返回后投影 `service.Events`），所以任何客户端都**观察不到
"执行开始"这一事件**。可依赖的是执行窗口本身——它等于 `tools.*.fake_delay_ms` 配置
的时长，只要取消落在这个窗口内就必然生效；落在窗口外则 Run 正常完成，用例会**明确失败**
（期望 `canceled` 却得到 `completed`），不会静默通过。

`fake_delay_ms` 只影响 fake 实现，用来模拟真实依赖的耗时，真实实现忽略该字段。

## 闭环：需求 → 用例 → 报告 → 待办 → 补用例

单跑一次评测是**单向**的：跑完、报完、结束。同一个缺陷因此可以再次逃逸。闭环靠三件事合起来：

**① 需求必须对得上用例。** `eval/coverage.json` 是人工评审过的映射，每条 claim 要么指向用例，要么显式豁免并写明理由：

```bash
go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v2.json \
  -check-coverage ./eval/coverage.json -config ./config.example.toml
# coverage claims=10 covered=8 waived=2 cases_without_claim=0
```

CI 会跑这一步。指向不存在的用例、或既豁免又映射、或既不映射也不豁免，都以退出码 `2` 失败。**没有任何 claim 指向的用例只是提示，不是失败**——用例可以服务多个 claim，或纯粹是额外回归。

**② 失败必须变成待办。** 失败时除了报告，还会输出归因：

```bash
go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v2.json \
  -report ./tmp/eval-report.json -triage ./tmp/eval-triage.json \
  -config ./config.example.toml
```

| Case | 断言 | 失败类别 | 先看哪里 |
| --- | --- | --- | --- |
| positive-audience-query | result | business_expectation | 用例声明的期望值，或实现返回的业务字段/事件序列 |

归因只从**失败类别**推导"先看哪里"，不做猜测：类别→怀疑对象的映射是固定的七条，未知类别明确标注"需人工判断"而不是丢掉。机器可读版本进 CI artifact。

**③ 修完必须补一个能抓住它的用例。** 这条由 `./eval/mutation-gate.sh` 支撑：把刚修好的那处再次改坏，新用例必须变红。**没有变红就说明用例没抓住它**，那就等于没补。

这三件事缺任何一件，环就是断的：只有 ① 会变成纸面覆盖；只有 ② 会变成"报完就完了"；只有 ③ 会缺少"该验什么"的输入。

## 风险与失败回流

PR 风险和影响标签由人声明；无法判断时按 high。高风险变更必须运行全量 Case，并由人工 reviewer 记录批准结论、理由和时间。`eval/feedback.go` 的 `RiskRecord`、`FailureRecord`、`HumanDisposition` 和 `JudgeResult` 只是 JSON 合同：Judge 默认关闭或使用本地 fake，永远不改变硬门禁退出码。

CLI 使用 `-risk low|medium|high` 和 `-impact-tags tag-a,tag-b`；也接受 `EVAL_RISK_LEVEL`、`EVAL_IMPACT_TAGS`。low/medium 运行对应风险基础 Case 加标签匹配 Case，high 或未知风险运行全量。CI 通过 `PR_RISK_LEVEL`/`PR_IMPACT_TAGS` 接线，未设置时按 high 全量运行；托管平台 Required Checks/Reviewers 仍需单独配置。

失败先按业务预期、代码实现、架构边界、评测规则、Case 数据、环境/依赖或 Judge 误判分类。只有人工 disposition 确认后，才允许增加版本化 Case、确定性规则或 Rubric；下一次关联回归必须执行新版本。PR 审批与失败校准是两条独立流程。

## 可选观测

Case 请求会携带低基数的 Case/代码/evaluator 版本到 HTTP span。OTLP/Langfuse、日志文件和 Trace snapshot 只作为诊断证据，不替代本地 JSON 报告；观测后端不可用不得改变业务状态。仓库里的 CI workflow 也不等于托管平台 Required Checks/Required Reviewers 已启用。

## 本地验证

```bash
go run ./cmd/archcheck
go test ./eval/...
go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v2.json -report ./tmp/eval-report.json -code-version dev -config ./config.example.toml
go run ./cmd/eval -dataset ./eval/datasets/synthetic-operations-v2.json -report ./tmp/eval-report-low.json -risk low -impact-tags audience-query -code-version dev -config ./config.example.toml
go run ./cmd/eval -dataset ./eval/datasets/feature-acceptance-template.json -report ./tmp/eval-report-template.json -code-version dev -config ./config.example.toml
```

2026-10-01 实际执行的第一条命令输出（省略 GIN 启动日志）：

```text
evaluation dataset=synthetic-operations@1 scenario=runtime-fake cases=8 passed=8 failed=0
  positive-audience-query          PASS terminal=completed writes=0
  positive-serial-summary          PASS terminal=completed writes=0
  diversity-audience-subject-variant PASS terminal=completed writes=0
  boundary-approved-outreach       PASS terminal=completed writes=1
  negative-reject-outreach         PASS terminal=failed writes=0
  negative-unknown-capability      PASS terminal=failed writes=0
  negative-invalid-input           PASS terminal= writes=0
  boundary-cancel-outreach         PASS terminal=canceled writes=0
```

摘要行显式打印 `scenario=runtime-fake`，避免把本地 fake 结果读成真实模型能力。报告中的 `evaluation_profile` 为：

```json
{
  "evaluation_plane": "runtime",
  "scenario": "runtime-fake",
  "model_provider": "fake",
  "model_name": "fake-supervisor@1",
  "executor": "local-fake",
  "verifier": "deterministic",
  "network_mode": "disabled",
  "tier": "pr"
}
```

Langfuse 多角度运行必须在原定运行链路和本地 Case 通过后单独执行、单独记录；配置存在或 exporter 初始化不能作为评测完成证据。

## 分层门禁与改坏验证

门禁分两层，都由确定性代码判定，不依赖模型、网络或人工判断；两层都必须绿才算通过：

| 层 | 内容 | 命令 |
| --- | --- | --- |
| **L0** | 格式、静态检查、依赖方向、单元与合同测试（含 race） | `gofmt -l cmd internal eval`、`go vet ./...`、`go run ./cmd/archcheck`、`go test ./...`、`go test -race ./...` |
| **L1** | 运行时验收用例（真实 HTTP/SSE 链路） | `go run ./cmd/eval -dataset ... -config ...` |

退出码语义（`0` 全部通过 / `1` 确定性断言失败 / `2` setup error）只有**构建出的二进制**看得准：`go run ./cmd/eval` 会把任何非 0 退出码统一折叠成 `1`，只在 stderr 打印 `exit status N`。需要核对 `1` 与 `2` 的区别时先 `go build -o ./tmp/eval ./cmd/eval`，再直接运行它；只关心 CI 是否失败则 `go run` 足够。

CI 会同时跑这两层，并在**完整数据集**上额外跑一次基线回归门禁（见下文），把 JSON/JSONL 报告作为 artifact 上传——**即使门禁失败也上传**，因为失败时才是最需要证据的时候。报告产物的保留期是 30 天。

### 证明门禁不是摆设

一个不会变红的门禁等于没有门禁。`eval/mutation-gate.sh` 对每一条受保护的行为注入**真实故障**：直接改源码 → 跑门禁 → 断言它必须变红 → 立即从备份还原（不使用 `git checkout`，避免影响未提交改动）。

```bash
./eval/mutation-gate.sh                # 全部注入
./eval/mutation-gate.sh approval       # 只跑名字匹配的一条
L1_ONLY=1 ./eval/mutation-gate.sh      # 只跑 L1，跳过 go test，快
```

覆盖的注入点与它们破坏的保护：

| 注入 | 被破坏的保护 |
| --- | --- |
| `policy-allowlist` | Worker/Tool 白名单（Tool 越权） |
| `approval-bypass` | 副作用必须经过审批 |
| `final-guard-off` | 敏感输出（凭证/Prompt/内部路径）拦截 |
| `terminal-uniqueness-off` | 每个 run 只能有一个终态事件 |
| `cancel-signal-lost` | 取消信号生效 |
| `idempotency-dedupe-off` | 相同幂等键只写一次 |

输出是一张 `mutation × L0 × L1` 的矩阵。**只要有一条注入没被抓到，脚本以非 0 退出并指名该行**，提示这条保护当前缺少覆盖。

两条使用纪律：

- 报"未抓到"之前，先确认注入本身有效——即这条保护失效后行为确实变了。注入点选错（例如改到了与故障无关的代码路径）会得到假的"未抓到"，比不跑更糟。
- 门禁抓不住的那条保护，就是下一步要补的测试或验收用例，不是可以忽略的噪音。

## 基线回归门禁

只有"本次全绿"一个信号会漏掉"修好一个、弄坏一个"：必须落到用例粒度比较。基线是一份**已批准**的逐用例判定快照，随仓库版本化保存在 `eval/baselines/`。

```bash
# 退出码 1（退化）与 2（不可比）只有二进制能区分：go run 会把两者都折叠成 1
go build -o ./tmp/eval ./cmd/eval

# 从一次真实通过的运行记录基线（失败的运行会被拒绝）
./tmp/eval -dataset ./eval/datasets/synthetic-operations-v2.json \
  -report ./tmp/eval-report.json -config ./config.example.toml \
  -write-baseline ./eval/baselines/synthetic-operations-v2.json

# 相对基线跑门禁（完整数据集）
./tmp/eval -dataset ./eval/datasets/synthetic-operations-v2.json \
  -report ./tmp/eval-report.json -config ./config.example.toml \
  -baseline ./eval/baselines/synthetic-operations-v2.json
```

比较结论只有四类，语义固定：

| 结论 | 条件 | 是否失败 |
| --- | --- | --- |
| `regression` | 基线通过、本次失败 | **是** |
| `missing` | 基线中有、本次未运行 | **是** |
| `new` | 本次新增、基线中没有 | 否，但会打印出来供人工复核 |
| `still_failing` | 基线中本来就失败、本次仍失败 | 否，避免把老问题反复报成新退化 |

退出码：一致 `0`；出现 `regression` 或 `missing` 为 `1`；dataset / evaluator / report schema 版本不一致为 `2`（`evaluation_setup_error`）。这里说的是**二进制**的退出码；`go run` 会把 `1` 与 `2` 都折叠成 `1`。

版本一致性检查在**运行任何 Case 之前**完成，因此不可比的运行不会留下报告；这与"setup error 不写报告"的既有约定一致。不提供 `-baseline` 时行为与以前完全一致。

### 基线与"窄选集"不能混用

`-risk` / `-impact-tags` 会缩小本次运行的用例集合，而 `missing` 的含义正是"基线中有、本次未运行"。两者叠加时未被选中的用例会全部报成 `missing` 并让门禁失败（实测 `-risk low` 得到 `missing=6`、退出码 `1`），原因与回归无关。因此 **CI 的基线步骤固定跑完整数据集**（`-risk high`，即 fail-safe 全量），风险窄选集继续走原来的验收步骤。

CI 在验收用例之后单独跑这一步，所以"相对已批准基线有没有退化"与"基线是否已经和 dataset/evaluator 版本脱节"都会在 PR 上直接失败，而不是只留在本地手工命令里。

### 快照记录判定身份，不只是标签

每个用例在快照里同时记 `passed` 与 `verdict_digest`；digest 覆盖观察到的事件、终态与结果投影，且**不含按请求生成的 trace id**，因此同一份代码独立跑两次得到相同 digest。

- 门禁只看 `passed`：digest 变化不会把绿灯判成红灯，也不会把红灯判成绿灯。
- 重新批准（`-write-baseline`）看整份"测量身份"：dataset/evaluator/report schema 版本与逐用例的 `case_id`、`case_version`、`passed`、`verdict_digest` 全部一致时才沿用原批准时间。正常重跑不产生 diff；"标签相同、证据变了"会以一次新的批准暴露出来。

## GORM 与 Langfuse

MySQL 适配器通过 GORM OpenTelemetry plugin 产生数据库查询 Span。运行期 Tool
调用会把 HTTP/Run 的 context 传入 GORM，因此 `SELECT`、`INSERT` 等数据库
Observation 可以和同一个 Run Trace 关联，并在启用 Langfuse OTLP 后上传到
Langfuse。查询参数被关闭，不把原始 SQL 参数、用户内容或凭证上传。

这不是普通 GORM 文本日志上传：普通 `slog` JSON 日志仍写 stderr/本地文件，
GORM logger 只保留错误/慢查询并启用参数化输出，适合本地诊断；Langfuse 接收的
是结构化 Trace/Observation。需要中央日志检索时，应使用 OTel Logs 或日志平台，
不把原始 SQL 日志塞进 Trace。

`cmd/eval` 默认关闭外部观测以保持零凭证本地门禁。需要一次显式 Langfuse 评分
上报时，可使用 `-langfuse-upload`，并提供 `LANGFUSE_API_URL`、
`LANGFUSE_PUBLIC_KEY`、`LANGFUSE_SECRET_KEY` 以及启用 Langfuse OTLP 的配置。
Reporter 只上传已有 TraceID 的 evaluator Boolean Score；本地 fake Case 没有
外部 Trace 时会跳过上传，上传失败也不会改变本地报告退出码。

上传内容是**由构造限定**的，不依赖 evaluator 自觉：每个 Score 只带固定字段
（`traceId`、evaluator 名称、Boolean 值、`dataType`）和封闭的 metadata 键集
（`case_id`、`case_version`、`code_version`、`evaluator_version` 以及 profile 的
`evaluation_plane`/`scenario`/`tier`）；失败 Score 的 `comment` 由 Reporter 自己
从 `failed_assertion` 和 `failure_taxonomy` 拼出，例如
`failed_assertion=result taxonomy=business_expectation`，**不转发** evaluator 的
`Reason` 自由文本。原因是 `Reason` 在本地报告里可以合法包含 evidence 值以便排障，
但 FR-008 要求 Langfuse/OTel 集成不得接收 Prompt、Tool payload、SQL 参数、凭证或
主机绝对路径；因此详细原因只留在本地 JSON 报告，sink 只拿结构化结论。

上传目标必须是本仓库自己的 Langfuse project。`http://localhost:3001` 是父
`suanming-agent` 工作区的共享实例（compose 位于
`/home/huang/workspace/suanming-agent/deploy/langfuse/`），其 `agent-runtime`
project 承载父项目自己的 agent 评测历史；本模板写入组织 `suanming-local` 下的
独立 project `eino-supervisor-template`，两边 traces/scores 互不可见。跑
`-langfuse-upload` 前先确认凭证归属：

```bash
curl -s -u "$LANGFUSE_PUBLIC_KEY:$LANGFUSE_SECRET_KEY" "$LANGFUSE_API_URL/api/public/projects"
# 期望 name=eino-supervisor-template；若出现 agent-runtime 说明拿错凭证，停止上传
```

本机凭证放在仓库根目录被 gitignore 的 `.env`；`cmd/server` 会自动加载，`cmd/eval`
不会，需要先 `set -a; . ./.env; set +a`。注意 `LANGFUSE_OTLP_HEADERS` 的值含空格，
在 `.env` 里必须加引号，否则会被 shell 截断成 `Authorization=Basic`，OTLP 导出报
`401 Invalid authorization header`，该错误还会被写进 evidence 并把用例判失败。

## JSON、数据库与日志后置断言

Case 可以额外声明 `postconditions.database` 和 `postconditions.logs`：

```json
{
  "postconditions": {
    "database": {
      "backend": "mysql",
      "before_order_count": 0,
      "after_order_count": 1,
      "expected_orders": [{"user_id": 1, "product_id": 1, "quantity": 2, "total_amount": "39.80"}]
    },
    "logs": {"min_records": 1, "required_phases": ["tool"]}
  }
}
```

Runner 会在请求前后查询已装配的 MySQL Adapter，并在报告中只保留行数和
规范化字段摘要哈希；不会写入完整数据库记录、SQL 或参数。没有启用 MySQL
时，声明数据库后置断言会硬失败，而不是降级为通过。日志后置断言读取本地
JSONL `slog` 的低基数字段（`phase`、`error_code`），会拒绝包含 SQL、凭证、
路径或用户原文的记录。该证据用于本地验收；Langfuse 仍只接收 Trace/Score，
不把普通 GORM 文本日志当作业务正确性结论。

## 反绕过合同

`eval/evaluator/evaluator_test.go` 覆盖规则选择、缺失 evidence、缺失 Policy evidence、非单调序号、重复终态、匿名 side-effect Tool 的审批前置、同一步不同 Worker/Tool 的审批借用、只读 Tool 写入归因和重复 side-effect 调用。它们只验证证据合同，不把 fake write count、Tool 名称或配置字段当成业务执行本身。
