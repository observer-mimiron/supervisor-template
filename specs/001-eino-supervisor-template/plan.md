# Implementation Plan: Eino Supervisor Template

**Branch**: `001-eino-supervisor-template`  
**Date**: 2026-09-20  
**Spec**: [spec.md](./spec.md)  
**Constitution**: [../../.specify/memory/constitution.md](../../.specify/memory/constitution.md)  
**Architecture**: [../../docs/architecture.md](../../docs/architecture.md)  
**Progress**: [../../PROGRESS.md](../../PROGRESS.md)  
**Tasks**: [./tasks.md](./tasks.md)

## Summary

实现一个可独立运行的 Go/Eino 运营 Agent 参考模板。模板只包含一个 Supervisor、一个有界 Worker、一个只读 Tool 和一个模拟副作用 Tool；通过确定性的 Policy Gate、审批、幂等、checkpoint 和事件投影，验证有界执行和可靠恢复，不携带命理或真实运营业务。

M0-M3 已完成并通过本地合同测试；M4 仍是后续阶段。实现顺序按 M0-M4 五个可独立验收模块拆分；每个模块完成后都能运行对应的零模型或 fake 合同测试，后续模块只能依赖前面已经通过的合同。目录和依赖方向以 [architecture.md](../../docs/architecture.md) 为唯一来源，当前状态以 [PROGRESS.md](../../PROGRESS.md) 为准，原子任务以 [tasks.md](./tasks.md) 为准。

## Technical Context

**Language/Version**: Go 1.25  
**Primary Dependencies**: Go module、Gin、Eino 0.9.12、Eino DeepSeek 0.1.6、BurntSushi TOML 和 OpenTelemetry 1.43.0  
**Storage**: v1 使用内存 Repository 和 checkpoint；持久化替换属于 M4，不是当前实现事实  
**Testing**: `go test ./...`、合同测试、HTTP/SSE smoke、必要的 race 测试  
**Target Platform**: Linux server / local development  
**Project Type**: Go HTTP/SSE Agent service/template  
**Performance Goals**: fake model 下 P1 只读流程从请求到终态小于 5 秒  
**Constraints**: M0-M2 不接真实 CRM、MCP 或外部写入；步骤、时间、调用次数有上限；副作用必须审批和幂等  
**Scale/Scope**: 单 Supervisor、单 Worker、一个只读 Tool、一个模拟副作用 Tool、单审批人、单进程  

**Configuration**: `-f` 指定 TOML 文件，随后应用环境变量覆盖，最后执行启动校验；默认重启生效，v1 不做热加载。部署参数、模型参数、Prompt 路径、能力开关、工具白名单、预算、超时、重试、审批和观测参数必须配置化。安全不变量、Schema、状态转换、权限上限、Policy Gate、审批和状态所有权必须由代码固定，配置不得创造未注册实现或降低安全要求。Gin 只负责 HTTP/SSE，Eino 只通过 `internal/infrastructure/eino` 接入；详细目录规则见 [architecture.md](../../docs/architecture.md)。

## Constitution Check

**初始检查：PASS（文档层）**

- 领域合同和状态归 `domain`；Eino、Gin、模型、存储、MCP 和可观测性只进入 `infrastructure`。
- Supervisor 只产生候选决策；权限、审批、重试、幂等、状态转换和终态由确定性代码负责。
- M0-M4 均有独立验收路径；M0-M3 已通过本地合同测试和受限本地 smoke，M4 保留为后续扩展。
- 配置和 Prompt 不能注册未批准能力、扩大权限或降低策略要求。
- 不新增顶层目录，不修改宪法；本计划引用 `.specify/memory/constitution.md` 这一唯一宪法来源。
- 未发现需要例外或 ADR 的复杂度。

**设计复核：PASS**

- `research.md`、`data-model.md`、`contracts/http-sse.md` 和 `quickstart.md` 与本计划及 `spec.md` 一致。
- 每个模块的完成条件都是可执行验证，不以“代码已写”作为验收。

## Module Plan

### M0：项目骨架与基础合同

**目标**：建立可启动的最小运行骨架和领域合同，不执行真实业务。

**主要落点**：以 [architecture.md](../../docs/architecture.md) 为准，任务拆分见 [tasks.md](./tasks.md)。M0 涵盖 `cmd/`、`internal/domain/`、`internal/application/`、`internal/config/`、`internal/composition/` 和内存基础设施。

**验收条件**：

- 未知 Worker、Tool、状态转换和越权风险均被确定性拒绝。
- 每次请求都有稳定 `run_id`；终态不可变；已完成步骤不可被覆盖。
- 配置按 `-f 文件 -> TOML -> 环境变量 -> 启动校验` 生效，非法配置在启动时失败。
- `go test ./internal/domain/... ./internal/application/... ./internal/infrastructure/...` 通过。

### M1：最小可运行链路

**目标**：完成 P1，固定首条链路 `Gin -> Application -> Eino Supervisor -> fake Worker -> fake Tool -> RunEvent -> SSE`。

**主要落点**：`internal/infrastructure/eino`、`internal/infrastructure/llm`、`internal/infrastructure/tool`、`internal/interfaces/http` 和 `internal/application/chat`；MCP 不属于 M1。

**验收条件**：

- 合法只读请求得到结构化结果和唯一 `completed` 事件。
- 未匹配能力不猜测目标，返回澄清或分类拒绝。
- 只读 Tool 之外的能力不会被调用。
- fake model 下端到端流程小于 5 秒；P1 合同测试通过。

### M2：策略、终态与可靠性

**目标**：完成 P2/P3 基础，加入 Policy Gate、Final Guard、审批、幂等、取消、超时、终态和中断恢复。

**主要落点**：`internal/domain/agent`、`internal/domain/approval`、`internal/application/run`、`internal/application/approval`、`internal/infrastructure/checkpoint`。

**验收条件**：

- 未批准时模拟写入计数始终为 0，并产生 `approval_required`。
- 拒绝后执行终止且不调用副作用 Tool。
- 批准后重复提交 10 次，副作用执行计数保持为 1，返回同一结果。
- 审批和幂等合同测试通过。

### M3：真实依赖适配

**目标**：接入真实 ChatModel、真实 Tool、Callback 和 OpenTelemetry，同时保留 fake 和零模型合同测试。

**主要落点**：`internal/infrastructure/llm`、`internal/infrastructure/tool`、`internal/infrastructure/observability`、`internal/composition`。

**验收条件**：

- 已完成步骤恢复时不再次调用。
- 可恢复、等待审批、失败和终态状态可区分。
- 终态 run 的 resume 只重放原结果，不重新执行 Tool。
- checkpoint 恢复、取消和重复恢复测试通过。

### M4：生产扩展与运营业务

**目标**：在需求、ADR 和合同齐备后接入持久化 checkpoint、MCP、认证、多租户和具体运营业务模块。

**主要落点**：`internal/infrastructure/checkpoint`、`internal/infrastructure/mcp`、`internal/interfaces/`、`internal/application/`、`internal/domain/operation/`、`docs/`。

**验收条件**：

- 事件 `sequence` 单调递增，每次 run 最多一个终态事件。
- 公开错误不包含凭证、Prompt、stack trace 或内部路径。
- 覆盖结构化模型失败、未注册能力、审批拒绝、Tool 超时和取消。
- `go test ./...`、HTTP/SSE smoke、必要的 `go test -race ./...` 通过。

## Project Structure

目录职责、禁止事项和依赖方向唯一见 [../../docs/architecture.md](../../docs/architecture.md)；本计划不复制另一套目录表。该架构明确 `interfaces -> application -> domain`、`composition -> application + domain + infrastructure`、`infrastructure -> domain/application contracts`，并禁止新增顶层 `workers/`、`tools/`、`state/` 或 `agentflow/`。

## Documentation Deliverables

- [research.md](./research.md)：技术和边界决策。
- [data-model.md](./data-model.md)：实体、状态和不变量。
- [contracts/http-sse.md](./contracts/http-sse.md)：HTTP/SSE 对外合同。
- [quickstart.md](./quickstart.md)：本地运行和五个模块的验证入口。
- [../../docs/architecture.md](../../docs/architecture.md)：目录职责、固定运行链路和依赖方向的唯一来源。
- [../../docs/technology-baseline.md](../../docs/technology-baseline.md)：技术栈、基础设施边界、配置合同和升级规则。
- [../../PROGRESS.md](../../PROGRESS.md)：当前事实快照，不把计划状态写成代码完成。
- [tasks.md](./tasks.md)：M0-M4 的原子任务、依赖、完成标准和验收命令。

## Complexity Tracking

None. v1 的内存实现、fake Tool 和单进程执行是有意限制，避免在业务合同稳定前引入数据库、消息队列或分布式恢复。
