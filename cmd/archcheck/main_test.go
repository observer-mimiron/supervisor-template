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
