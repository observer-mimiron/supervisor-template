// Package tool 提供模板的 Tool 注册表和基础设施实现。
//
// 本文件只声明已注册能力及其风险，不改变 Policy Gate 的权限判断。
package tool

import (
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
)

// Contracts 返回默认 fake 配置下的 Tool 合同。
func Contracts() []domaintool.Contract { return ContractsFor(examplebusiness.ReadOnlyToolFake) }

// ContractsFor 按装配层选择返回 user_query 的实现声明。
func ContractsFor(userQueryImplementation string) []domaintool.Contract {
	return examplebusiness.ToolContracts(userQueryImplementation)
}
