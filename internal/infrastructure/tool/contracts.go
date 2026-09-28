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

// ContractsForMySQL adds the optional database Tool contracts to the fixed
// example registry. It is selected only during startup when a MySQL adapter is
// actually configured.
func ContractsForMySQL(userQueryImplementation string) []domaintool.Contract {
	contracts := ContractsFor(userQueryImplementation)
	return append(contracts,
		domaintool.Contract{ToolID: examplebusiness.MySQLQueryToolID, Implementation: examplebusiness.MySQLQueryImpl, Risk: "read_only", RetryLimit: 1, RequiredInputs: []string{"message"}, MaxInputBytes: 64 << 10, MaxOutputBytes: 64 << 10},
		domaintool.Contract{ToolID: examplebusiness.MySQLInsertToolID, Implementation: examplebusiness.MySQLInsertImpl, Risk: "side_effect", RequiresApproval: true, IdempotencyRequired: true, RequiredInputs: []string{"message"}, MaxInputBytes: 64 << 10, MaxOutputBytes: 64 << 10},
	)
}
