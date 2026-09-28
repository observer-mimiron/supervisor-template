# Quickstart: Local Case Evaluation

本指南定义本地验收闭环：人工 JSON 数据集 -> 现有 HTTP/SSE 业务链路 -> 结构化证据 -> 确定性 Evaluator -> JSON 报告。它借鉴 `/home/huang/workspace/suanming-agent/eval` 的数据集和报告模式，但不要求该项目的 Langfuse 服务、凭证或脚本。

## Prerequisites

- Go 1.25+
- 不需要 Docker、模型凭证、Langfuse、Apifox、外部 Tool 或网络

可选 MySQL 运行链路使用宿主机 Go 加 Compose 依赖：

```bash
: "${MYSQL_ROOT_PASSWORD:?set MYSQL_ROOT_PASSWORD}"
: "${MYSQL_PASSWORD:?set MYSQL_PASSWORD}"
: "${MYSQL_ORDER_DSN:?set MYSQL_ORDER_DSN}"
docker compose up -d mysql
```

默认 Compose 镜像使用 DaoCloud 镜像代理；受限网络可通过 `MYSQL_IMAGE` 或 `OTEL_IMAGE` 覆盖为可访问的镜像地址。

## Run the local dataset

```bash
go run ./cmd/eval \
  -dataset ./eval/datasets/synthetic-operations-v1.json \
  -report ./tmp/eval-report.json \
  -code-version dev
```

预期退出码：全部硬评测通过为 `0`；运行或断言失败为 `1`；数据集或 Runner 装配无效为 `2`。报告包含 dataset/Case version、代码 revision、生成时间、每个 run ID、事件/Tool/审批/终态证据、分项 evaluator 结果和失败分类；默认不保存完整用户响应或 Tool payload。

Runner 为每个 Case 使用唯一 run/conversation，单个总 timeout 覆盖该 Case 的所有请求；重复执行不得共享 fixture 状态。Case 的 `expected_results` 和 evaluator 规则只由 JSON 数据集定义，不会由 Runner 或模型修改。

## Dataset and evaluator checks

```bash
go run ./cmd/archcheck
go test ./eval/...
go test ./... -run 'Evaluation|Case|Evaluator' -count=1
```

当前版本 Dataset 实际覆盖正向只读查询、两步汇总、diversity subject、审批后触达/重复 resume、拒绝触达、未知能力、空输入和等待审批取消八个 Case。跨主体访问、超时和失败注入仍由运行时合同测试覆盖，尚未全部纳入这份展示 Dataset；不要把计划中的 Case 清单写成当前评测结果。

## Full local verification

```bash
go test ./...
go test -race ./...
go build ./cmd/server/
go vet ./...
git diff --check
```

## Optional server smoke

默认配置使用 DeepSeek；同目录 `.env`/环境变量可覆盖 provider，启动日志会打印实际的 `model_provider`：

```bash
export AGENT_AUTH_DEMO_TOKEN_SHA256="$(printf %s 'demo-token' | sha256sum | awk '{print $1}')"
go run ./cmd/server/ -f ./config.example.toml
```

无外部模型的 fake HTTP smoke 必须显式使用 `-fake`：

```bash
go run ./cmd/server/ -f ./config.example.toml -fake
```

验证 `/healthz`、正向 `/api/chat`、未审批副作用不写入、批准后 resume、重复 resume/重复触达只写一次、跨主体拒绝、cancel、deadline/Tool timeout 和失败评测报告。审批与恢复使用现有 `/api/runs/{run_id}/approval`、`/resume` 路由，不新增业务 endpoint。

## Failure feedback

按报告中的 `evidence_reference` 重放失败 Case，由人工标注失败原因：业务预期、代码实现、架构边界、评测规则、Case 数据、环境/依赖或 Judge 误判。人工确认后再选择增加 Case、确定性规则或更新 Rubric，并递增相应版本；不得自动把所有失败样本写回 Dataset。

## PR risk and merge gates

每个 PR 由提交者声明风险和影响标签。普通变更运行基础验证及匹配标签的 Case；涉及 Policy Gate、身份/资源授权、审批、幂等、状态所有权、持久化、副作用或依赖方向时按高风险处理，运行全量 Case，并由人工 reviewer 留下批准结论和理由。未声明或无法判断时按高风险处理。失败样本 disposition 不代替 PR 人工审批。

CI 必须运行静态依赖检查、Go 测试、评测测试和本地 Case Runner；任何硬门禁失败均非零退出。需要在代码托管平台配置 Required Checks 和高风险变更的 Required Reviewers 后，才能称为合并阻断门禁。LLM Judge 只告警，不决定退出码。

Langfuse OTLP 关联是可选项，不参与默认 Case 通过判定；在线 LLM Judge、Apifox、真实 MySQL/GORM 和 CRM 写入需要额外合同或独立 Spec/Plan。
