package composition

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
	"github.com/observer-mimiron/supervisor-template/internal/domain/identity"
)

func TestNewBuildsHealthyMemoryGraph(t *testing.T) {
	cfg, err := loadExampleConfig()
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !app.Health.Healthy() {
		t.Fatal("expected healthy app")
	}
}

func TestNewRejectsMissingRuntimePromptReference(t *testing.T) {
	cfg, err := loadExampleConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.Supervisor.PromptFile = ""
	if _, err := New(cfg); err == nil {
		t.Fatal("expected startup registration failure")
	}
}

func TestNewRejectsBusinessCapabilityOutsideExplicitModule(t *testing.T) {
	cfg, err := loadExampleConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tools["extra_tool"] = config.ToolConfig{
		Enabled: true, Implementation: "fake.user_query", Risk: "read_only", Timeout: cfg.Tools["user_query"].Timeout,
	}
	if _, err := New(cfg); err == nil {
		t.Fatal("expected unregistered business tool rejection")
	}
}

func TestNewUsesStartupCatalogSnapshot(t *testing.T) {
	cfg, err := loadExampleConfig()
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if app.Close != nil {
		defer app.Close(context.Background())
	}
	cfg.Tools["user_query"] = config.ToolConfig{Enabled: true, Implementation: "unknown", Risk: "read_only"}
	runID, err := app.Run.Start(context.Background(), conversation.ExecutionRequest{
		RunID: "catalog-snapshot", ConversationID: "conversation-1",
		Subject: identity.Subject{TenantID: "test-tenant", SubjectID: "test-user"}, Message: "分析示例用户分群",
	})
	if err != nil {
		t.Fatal(err)
	}
	events := app.Run.Events(runID)
	if len(events) == 0 || events[len(events)-1].Type != agent.Completed {
		t.Fatalf("runtime changed after startup: %#v", events)
	}
}

func TestNewRoutesConfiguredHTTPReadOnlyToolThroughApplicationContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("message") == "" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte("外部只读结果"))
	}))
	defer server.Close()

	cfg, err := loadExampleConfig()
	if err != nil {
		t.Fatal(err)
	}
	userQuery := cfg.Tools["user_query"]
	userQuery.Implementation = "http.read_only"
	userQuery.Endpoint = server.URL
	cfg.Tools["user_query"] = userQuery
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if app.Close != nil {
		defer app.Close(context.Background())
	}
	runID, err := app.Run.Start(context.Background(), conversation.ExecutionRequest{
		RunID:          "run-http-tool",
		ConversationID: "conversation-1",
		Subject:        identity.Subject{TenantID: "test-tenant", SubjectID: "test-user"},
		Message:        "分析示例用户分群",
	})
	if err != nil {
		t.Fatal(err)
	}
	events := app.Run.Events(runID)
	if len(events) == 0 || events[len(events)-1].Type != agent.Completed {
		t.Fatalf("http read-only run did not complete: %#v", events)
	}
	var foundText bool
	for _, event := range events {
		if event.Type == agent.Text && event.Data["content"] == "外部只读结果" {
			foundText = true
		}
	}
	if !foundText {
		t.Fatalf("external tool result was not projected: %#v", events)
	}
}

func loadExampleConfig() (config.Config, error) {
	return config.Load(filepath.Join("..", "..", "config.example.toml"))
}
