package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientAllowListAndSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"user_query","result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	defer server.Close()

	client, err := NewClient(map[string]Server{"approved": {Endpoint: server.URL, AllowedTools: []string{"user_query"}}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "blocked", "user_query", nil); Classify(err) != ErrorDenied {
		t.Fatalf("expected allow-list denial, got %v", err)
	}
	got, err := client.Call(context.Background(), "approved", "user_query", map[string]string{"q": "hello"})
	if err != nil || got != "ok" {
		t.Fatalf("call = %q, %v", got, err)
	}
}

func TestClientClassifiesTimeoutProtocolAndBusinessErrors(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer slow.Close()
	client, err := NewClient(map[string]Server{"slow": {Endpoint: slow.URL, AllowedTools: []string{"user_query"}}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Call(ctx, "slow", "user_query", nil); Classify(err) != ErrorTimeout {
		t.Fatalf("expected timeout, got %v", err)
	}

	for name, body := range map[string]string{
		"protocol": "not-json",
		"business": `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"rejected"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client, err := NewClient(map[string]Server{"one": {Endpoint: server.URL, AllowedTools: []string{"user_query"}}}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Call(context.Background(), "one", "user_query", nil)
			want := ErrorProtocol
			if name == "business" {
				want = ErrorBusiness
			}
			if Classify(err) != want {
				t.Fatalf("class = %q, want %q (%v)", Classify(err), want, err)
			}
		})
	}
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	}))
	defer large.Close()
	client, err = NewClient(map[string]Server{"large": {Endpoint: large.URL, AllowedTools: []string{"user_query"}}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "large", "user_query", nil); Classify(err) != ErrorProtocol {
		t.Fatalf("oversized response class = %q", Classify(err))
	}
}

func TestClientRejectsOversizedAndInvalidArguments(t *testing.T) {
	client, err := NewClient(map[string]Server{"one": {Endpoint: "http://127.0.0.1:1", AllowedTools: []string{"user_query"}}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "one", "", nil); Classify(err) != ErrorProtocol {
		t.Fatalf("empty tool id class = %q", Classify(err))
	}
	if _, err := client.Call(context.Background(), "one", "user_query", map[string]string{"q": strings.Repeat("x", maxArgumentBytes)}); Classify(err) != ErrorProtocol {
		t.Fatalf("oversized args class = %q", Classify(err))
	}
	if _, err := client.Call(context.Background(), "one", "user_query", map[string]string{"q\n": "x"}); Classify(err) != ErrorProtocol {
		t.Fatalf("invalid args class = %q", Classify(err))
	}
}
