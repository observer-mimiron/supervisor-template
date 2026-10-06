package main

import "testing"

func TestForbiddenDependencyRules(t *testing.T) {
	module := "github.com/observer-mimiron/supervisor-template/"
	tests := []struct {
		name     string
		pkg      string
		imported string
		want     bool
	}{
		{"cmd application", module + "cmd/server", module + "internal/application", true},
		{"cmd composition allowed", module + "cmd/server", module + "internal/composition", false},
		{"infrastructure config", module + "internal/infrastructure/tool", module + "internal/config", true},
		{"interfaces tool", module + "internal/interfaces/http", module + "internal/infrastructure/tool", true},
		{"interfaces observability exception", module + "internal/interfaces/http", module + "internal/infrastructure/observability", false},
		{"application domain allowed", module + "internal/application/run", module + "internal/domain/agent", false},
		{"domain infrastructure", module + "internal/domain/agent", module + "internal/infrastructure/tool", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := forbidden(test.pkg, test.imported); got != test.want {
				t.Fatalf("forbidden(%q, %q) = %v, want %v", test.pkg, test.imported, got, test.want)
			}
		})
	}
}

// TestNonStdlibDomainImport 守护 SC-007 的依赖一侧。
//
// forbidden 只看模块内前缀，因此领域包引入第三方库不会触发它；这条规则补上那个缺口。
func TestNonStdlibDomainImport(t *testing.T) {
	module := "github.com/observer-mimiron/supervisor-template/"
	tests := []struct {
		name     string
		pkg      string
		imported string
		want     bool
	}{
		{"stdlib single segment", module + "internal/domain/approval", "errors", false},
		{"stdlib with slash", module + "internal/domain/approval", "crypto/sha256", false},
		{"stdlib encoding", module + "internal/domain/approval", "encoding/hex", false},
		{"domain internal allowed", module + "internal/domain/agent", module + "internal/domain/operation", false},
		{"third party rejected", module + "internal/domain/agent", "github.com/cloudwego/eino/compose", true},
		{"orm rejected", module + "internal/domain/operation", "gorm.io/gorm", true},
		{"other internal layer rejected", module + "internal/domain/agent", module + "internal/config", true},
		{"non domain package ignored", module + "internal/infrastructure/tool", "github.com/cloudwego/eino", false},
		{"cmd package ignored", module + "cmd/server", "github.com/gin-gonic/gin", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nonStdlibDomainImport(test.pkg, test.imported); got != test.want {
				t.Fatalf("nonStdlibDomainImport(%q, %q) = %v, want %v", test.pkg, test.imported, got, test.want)
			}
		})
	}
}
