// Package operation 定义示例运营 Worker 的声明合同。
//
// 本包只保存 Worker 的能力描述，不实现 Worker，也不依赖 Agent 框架。
package operation

import "time"

// WorkerContract 描述一个有界 Worker 的输入、输出和 Tool 白名单。
type WorkerContract struct {
	WorkerID       string
	Implementation string
	AllowedTools   []string
	Timeout        time.Duration
}
