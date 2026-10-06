package composition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
	"github.com/observer-mimiron/supervisor-template/internal/domain/identity"
	httpapi "github.com/observer-mimiron/supervisor-template/internal/interfaces/http"
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

func TestNewFakeOutreachKeepsAudienceProjectionThroughResume(t *testing.T) {
	cfg, err := loadExampleConfig()
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	subject := identity.Subject{TenantID: "test-tenant", SubjectID: "test-user"}
	runID, err := app.Run.Start(context.Background(), conversation.ExecutionRequest{
		RunID:          "run-composition-outreach",
		ConversationID: "conversation-1",
		Subject:        subject,
		Message:        "模拟触达示例用户",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run.Approve(context.Background(), subject, runID, "", "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Run.Resume(context.Background(), subject, runID); err != nil {
		t.Fatal(err)
	}
	events := app.Run.Events(runID)
	if len(events) == 0 || events[len(events)-1].Type != agent.Completed {
		t.Fatalf("fake outreach did not complete: %#v", events)
	}
}

func TestHTTPFakeOutreachUsesConfiguredAuthAndCompletesAfterResume(t *testing.T) {
	cfg, err := loadExampleConfig()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("composition-http-token"))
	for index := range cfg.Auth.Credentials {
		if cfg.Auth.Credentials[index].SubjectID == "demo-user" {
			_ = os.Setenv(cfg.Auth.Credentials[index].TokenSHA256Env, hex.EncodeToString(digest[:]))
			defer os.Unsetenv(cfg.Auth.Credentials[index].TokenSHA256Env)
		}
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	server := httptest.NewServer(httpapi.NewRouter(app.Run, app.Health, app.Authenticator, httpapi.Options{}))
	defer server.Close()
	client := server.Client()
	post := func(path, body string) string {
		req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer composition-http-token")
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			t.Fatalf("%s status=%d body=%s", path, response.StatusCode, data)
		}
		return string(data)
	}
	post("/api/chat", `{"run_id":"http-composition-outreach","conversation_id":"http-composition-outreach","message":"模拟触达沉睡客户"}`)
	post("/api/runs/http-composition-outreach/approval", `{"decision":"approve"}`)
	body := post("/api/runs/http-composition-outreach/resume", `{}`)
	if !strings.Contains(body, `"type":"completed"`) {
		t.Fatalf("resume body=%s", body)
	}
}

func loadExampleConfig() (config.Config, error) {
	cfg, err := config.Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		return cfg, err
	}
	cfg.Model.Provider = "fake"
	cfg.Model.Name = "fake-model"
	return cfg, nil
}
