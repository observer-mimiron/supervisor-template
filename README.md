# Eino Supervisor Template

一个用 Go 和 Eino 搭建的有界 Agent 服务模板。模型只负责提出候选路由；认证、权限、审批、幂等、运行状态和最终事件由应用代码决定。

这个仓库的重点是把一条可检查的运行链路跑通：HTTP/SSE 请求经过应用层、Policy Gate、执行计划、Worker 和 Tool，最后变成有序的运行事件。示例业务是合成的，不连接真实 CRM、营销渠道或客户数据。

## 快速开始

需要 Go 1.25 或更高版本。依赖下载完成后，本地 fake 和评测不需要模型密钥、Docker 或外部服务。

### 跑本地检查

```bash
gofmt -l cmd internal eval
go vet ./cmd/... ./internal/... ./eval/...
go run ./cmd/archcheck
go test ./cmd/... ./internal/... ./eval/...
go test -race ./cmd/... ./internal/... ./eval/...
go build ./cmd/server/
```

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

当前数据集包含 13 个 Case。全部通过时输出 `passed=13 failed=0`，退出码为 `0`；断言失败为 `1`；数据集或装配无效为 `2`。需要区分 `1` 和 `2` 时使用构建出的二进制，因为 `go run` 会把非零退出码折叠成 `1`。

要验证门禁确实能拦住回归，可以运行：

```bash
./eval/mutation-gate.sh
```

脚本会临时注入故障，运行检查后恢复源码。它需要独占工作区。

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

## 运行边界

请求按下面的顺序处理：

```text
HTTP/SSE + AuthN
  -> Application + resource AuthZ
  -> Supervisor
  -> Policy Gate
  -> Manager / ExecutionPlan
  -> bounded Worker
  -> allow-listed Tool
  -> Final Guard
  -> RunEvent
  -> SSE projection
```

- Supervisor 和模型只能提交结构化候选，不能授权 Tool、修改运行状态或决定终态。
- Policy Gate 只允许启动时注册、且在 Worker allow-list 中的能力。
- Manager 持有 `ExecutionPlan` 和运行状态；Worker 没有最终答复权。
- 有副作用的 Tool 必须先获得动作级审批，再使用幂等键执行。重复 resume 不会重复写入。
- `/api/*` 先认证主体，再检查 run 的归属；客户端提交的 `reviewer` 不作为可信身份。
- 取消通过 `context.Context` 协作完成，不承诺强制终止不响应取消的外部进程。
- 每个 run 只产生一个公开终态事件。外部调用已经开始但结果未确认时，运行会进入未知结果状态，不自动重试。

事件会被保存后按顺序投影到 SSE；当前实现提供心跳和有序重放，不应把它理解为实时 token 流。

## 配置

配置文件是 [`config.example.toml`](./config.example.toml)。生效顺序为：

1. `-f` 指定的 TOML；
2. TOML 同目录的 `.env`；
3. 已存在的进程环境变量覆盖前两项；
4. `-fake` 最后把 provider 固定为 `fake`、模型固定为 `fake-model`。

配置只能选择已注册的 provider、Worker、Runner 和 Tool，不能通过配置新增能力、扩大 allow-list 或降低审批要求。相对路径资源（例如 `prompts/`）按配置文件所在目录解析。

常用环境变量包括 `MODEL_PROVIDER`、`LLM_MODEL`、`LLM_BASE_URL`、`FAKE_MODEL`、`LISTEN_ADDR`、`PERSISTENCE_BACKEND`、`MYSQL_ENABLED` 和 `OTEL_*`。完整字段和默认值以 `config.example.toml` 为准。

## 扩展示例业务

模板自带的示例业务集中在 [`internal/infrastructure/examplebusiness`](./internal/infrastructure/examplebusiness)。替换业务时通常需要：

1. 在 `registry.go` 声明 Worker、Route、Tool contract 和实现标识；
2. 在 `toolhandlers.go` 注册 Tool handler；
3. 在 `config.example.toml` 登记 Worker、Route 和 Tool；
4. 在 `prompts/` 添加使用 `eino_adk` Runner 所需的 prompt；
5. 在 `eval/datasets/` 和 `eval/coverage.json` 增加可重放的验收 Case。

`internal/composition/` 只负责注册和装配，不承载业务判断。新增能力仍要经过注册表、Policy Gate 和对应测试。

## 项目结构

| 路径 | 职责 |
| --- | --- |
| `cmd/server/` | 加载配置并启动 HTTP 服务 |
| `cmd/eval/` | 运行验收 Case、生成报告和基线结果 |
| `cmd/archcheck/` | 检查包依赖方向和配置字段消费方 |
| `internal/config/` | 配置解析、环境变量覆盖和启动校验 |
| `internal/domain/` | 领域合同和状态不变量 |
| `internal/application/` | 用例编排、运行状态和执行计划 |
| `internal/interfaces/` | HTTP/SSE 适配 |
| `internal/infrastructure/` | Eino、模型、Tool、存储、认证和观测适配器 |
| `internal/composition/` | 注册、实现选择和依赖装配 |
| `eval/` | Dataset、Runner、Evaluator、基线和 mutation gate |
| `prompts/`、`schemas/` | 版本化 prompt 和结构合同 |

## 当前状态

已实现并有本地验证：固定运行链路、Policy Gate、动作级审批、幂等、取消、文件/内存存储、确定性评测、基线回归和故障注入门禁。

仍是部分实现或明确延期：实时 SSE token 流、完整 OIDC/JWT/RBAC、多租户、跨进程 exactly-once、生产级 MySQL 状态后端、在线 LLM Judge、开放式 ReAct/Graph/DAG，以及真实 CRM 或营销触达。这里的 `fake` Tool 只用于合同验证，不代表外部业务已接入。

## 许可证

本项目原创代码和文档采用 [MIT License](./LICENSE)；第三方依赖按各自许可证使用。
