package tool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestHTTPReadOnlyToolUsesConfiguredGETEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if got := r.URL.Query().Get("message"); got != "只读查询" {
			t.Fatalf("message query = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Fatal("missing user agent")
		}
		_, _ = w.Write([]byte("外部只读结果"))
	}))
	defer server.Close()

	adapter, err := NewHTTPReadOnlyTool(server.URL+"/lookup?scope=demo", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Execute(context.Background(), map[string]string{"message": "只读查询"})
	if err != nil || result != "外部只读结果" {
		t.Fatalf("result=%q err=%v", result, err)
	}
}

func TestHTTPReadOnlyToolRejectsNonSuccessAndOversizedResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") == "large" {
			_, _ = w.Write(make([]byte, maxReadOnlyBodyBytes+1))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	adapter, err := NewHTTPReadOnlyTool(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Execute(context.Background(), map[string]string{"message": "query"}); err == nil {
		t.Fatal("expected non-2xx error")
	}

	parsed, _ := url.Parse(server.URL)
	query := parsed.Query()
	query.Set("mode", "large")
	parsed.RawQuery = query.Encode()
	adapter, err = NewHTTPReadOnlyTool(parsed.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Execute(context.Background(), map[string]string{"message": "query"}); err == nil {
		t.Fatal("expected oversized response error")
	}
}
