# Eino Supervisor Template

这是一个可独立运行的、有限范围的 Go Agent 参考模板。

设计基线：

- 参考成熟 Agent 平台的 DDD 分层：`api/application/domain/infra/crossdomain`
- 固定执行模式：`Supervisor -> Policy Gate -> Manager -> Worker -> Tool`
- Eino 的 `ChatModelAgent`、`Runner`、Tool calling、Callback 和 interrupt/resume

目录职责和固定运行链路见 [docs/architecture.md](./docs/architecture.md)。

当前实现使用本地 fake Supervisor、fake Tool、内存 Repository 和内存 checkpoint，覆盖只读请求、审批、副作用幂等、取消、超时、恢复和 SSE 投影；不连接真实运营系统。

技术栈已经锁定为 Go + Gin + Eino + TOML。Gin 负责 HTTP/SSE 边界，Eino 负责 Agent 适配，配置文件和环境变量负责部署参数；安全合同、状态机和权限仍由代码固定。`config.example.toml` 是配置入口示例。

## 本地运行

```bash
go test ./...
go test -race ./...
go run ./cmd/server/ -f ./config.example.toml
```

服务启动后可使用 `/api/chat`、审批、resume 和 cancel 接口验证运行链路。

技术选型、依赖放置和升级规则见 [docs/technology-baseline.md](./docs/technology-baseline.md)。

## 许可证

本项目原创代码和文档采用 [MIT License](./LICENSE)。第三方依赖按各自的许可证使用。
