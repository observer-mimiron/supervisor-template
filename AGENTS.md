# Repository Guidelines

## 项目结构

本仓库目前是 Go Agent/Supervisor 模板的设计阶段，尚未提交生产源码或 `go.mod`。
`docs/` 保存架构与技术基线，`specs/001-eino-supervisor-template/` 保存需求、计划、合同和任务，
`.specify/` 保存项目宪法与工作流，`config.example.toml` 是配置示例，`PROGRESS.md` 是当前事实快照。
实现完成后按架构约定使用 `cmd/server/`、`internal/{domain,application,interfaces,infrastructure,composition}/`、
`prompts/`、`schemas/` 和 `eval/`；不要另建 `workers/`、`tools/` 或 `state/` 顶层目录。

## 构建、测试与本地开发

M0 完成后运行 `go test ./...`，并用 `go test -race ./...` 做并发检查；启动示例服务使用
`go run ./cmd/server/ -f ./config.example.toml`，再按 Quickstart 执行 `/api/chat` SSE smoke。
当前代码阶段尚不能把这些命令标记为已通过。文档改动至少运行 `git diff --check`，并检查相对链接。

## 编码与命名

遵循 Go 官方格式化规则（`gofmt`），包名使用小写，导出标识符使用 Go 文档注释，测试文件命名为
`*_test.go`。保持依赖方向 `interfaces -> application -> domain`；Gin、Eino、数据库、MCP 和观测实现放在
`infrastructure`，`composition` 只负责装配，不承载业务判断。重要状态、权限和终态规则必须由确定性代码负责。

## 测试要求

优先编写本地 fake 模型/Tool 的合同测试，覆盖路由 fallback、Policy Gate 拒绝、输入校验、超时、恢复、
SSE 顺序和唯一终态。新增跨层行为必须提供可重放入口或合同测试；在线模型评测只能作为补充证据。

## 提交与合并请求

提交信息沿用历史中的 Conventional Commits 前缀，例如 `feat:`、`refactor:`、`docs:`，主题简短、使用祈使语气。
PR 应说明行为变化、影响目录、验证命令和未解决风险；涉及 SSE 或接口行为时附请求示例。若改变架构、依赖方向、
状态所有权或权限边界，先更新 `docs/architecture.md`、Spec/Plan 或 ADR，并同步 `PROGRESS.md`。

## 配置与安全

不要提交密钥、真实外部写入或未脱敏的 Prompt/用户数据。配置只能选择已注册实现，不能扩大权限、降低审批等级或绕过
Policy Gate；领域层不得依赖 HTTP、Eino、数据库、MCP、具体模型或 SSE。
