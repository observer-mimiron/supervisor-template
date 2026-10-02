// Package tool provides the generic Tool registry and bounded invocation pool.
package tool

import domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"

// CloneContracts copies registration contracts without importing a business
// module. Composition owns the source of the contracts.
func CloneContracts(contracts []domaintool.Contract) []domaintool.Contract {
	cloned := make([]domaintool.Contract, len(contracts))
	for index, contract := range contracts {
		cloned[index] = contract
		cloned[index].RequiredInputs = append([]string(nil), contract.RequiredInputs...)
	}
	return cloned
}
