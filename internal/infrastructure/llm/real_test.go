package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

type fixedChatModel struct {
	einomodel.ToolCallingChatModel
	content string
}

func (m fixedChatModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return schema.AssistantMessage(m.content, nil), nil
}

func TestRealSupervisorParsesStructuredDecisionAndOwnsDecisionID(t *testing.T) {
	supervisor := NewRealSupervisor(fixedChatModel{content: `{"decision_id":"model-controlled","worker_id":"user_analysis","intent":"query","arguments":{"tool_id":"user_query","message":"分析"},"risk":"read_only","confidence":0.9}`}, "", time.Second, defaultMaxSteps)
	decision, err := supervisor.Decide(context.Background(), conversation.ExecutionRequest{RunID: "run-1", Message: "分析"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.DecisionID != "run-1:decision" {
		t.Fatalf("decision id = %q, want run-1:decision", decision.DecisionID)
	}
	if decision.Arguments["tool_id"] != "user_query" || decision.Risk != "read_only" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestParseDecisionRejectsFencesProseAndTrailingData(t *testing.T) {
	base := `{"decision_id":"d1","worker_id":"user_analysis","intent":"query","arguments":{"tool_id":"user_query"},"risk":"read_only","confidence":1}`
	for name, raw := range map[string]string{
		"fence":    "```json\n" + base + "\n```",
		"prose":    "here is the decision: " + base,
		"trailing": base + "\n{}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDecision(raw, defaultMaxSteps); err == nil {
				t.Fatal("expected whole-response rejection")
			}
		})
	}
}

func TestParseDecisionRejectsUnknownFields(t *testing.T) {
	_, err := parseDecision(`{"decision_id":"d1","worker_id":"user_analysis","intent":"query","arguments":{"tool_id":"user_query"},"risk":"read_only","confidence":1,"approval":true}`, defaultMaxSteps)
	if err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestParseDecisionRejectsInvalidRouteShape(t *testing.T) {
	for name, raw := range map[string]string{
		"missing-tool":       `{"decision_id":"d1","worker_id":"user_analysis","intent":"query","arguments":{},"risk":"read_only","confidence":1}`,
		"invalid-risk":       `{"decision_id":"d1","worker_id":"user_analysis","intent":"query","arguments":{"tool_id":"user_query"},"risk":"admin","confidence":1}`,
		"invalid-confidence": `{"decision_id":"d1","worker_id":"user_analysis","intent":"query","arguments":{"tool_id":"user_query"},"risk":"read_only","confidence":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDecision(raw, defaultMaxSteps); err == nil {
				t.Fatal("expected strict route rejection")
			}
		})
	}
}

func TestNewDeepSeekSupervisorUsesOfficialAdapterAgainstLocalEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		var request struct {
			Thinking struct {
				Type string `json:"type"`
			} `json:"thinking"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		if request.Thinking.Type != "disabled" {
			t.Fatalf("thinking.type = %q, want disabled", request.Thinking.Type)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"local","object":"chat.completion","created":1,"model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"{\"decision_id\":\"model\",\"worker_id\":\"user_analysis\",\"intent\":\"query\",\"arguments\":{\"tool_id\":\"user_query\",\"message\":\"分析\"},\"risk\":\"read_only\",\"confidence\":0.8}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":8,"total_tokens":12}}`))
	}))
	defer server.Close()
	t.Setenv("TEST_LLM_API_KEY", "test-key")

	supervisor, err := NewDeepSeekSupervisor(context.Background(), ModelOptions{
		Name:        "deepseek-chat",
		BaseURL:     server.URL,
		APIKeyEnv:   "TEST_LLM_API_KEY",
		Temperature: 0,
		MaxTokens:   128,
		Timeout:     time.Second,
	}, "只输出 SupervisorDecision JSON")
	if err != nil {
		t.Fatal(err)
	}
	decision, err := supervisor.Decide(context.Background(), conversation.ExecutionRequest{RunID: "run-local", Message: "分析"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.DecisionID != "run-local:decision" || decision.Arguments["tool_id"] != "user_query" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
	if os.Getenv("TEST_LLM_API_KEY") != "test-key" {
		t.Fatal("test credential was not scoped to the environment")
	}
}
