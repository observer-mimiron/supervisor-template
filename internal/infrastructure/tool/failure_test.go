package tool

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/mcp"
)

func TestNormalizeInvocationError(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		kind  FailureKind
		class application.ErrorClass
	}{
		{name: "timeout", err: &mcp.Error{Class: mcp.ErrorTimeout, Err: context.DeadlineExceeded}, kind: FailureTimeout, class: application.ErrorTimeout},
		{name: "unavailable", err: &mcp.Error{Class: mcp.ErrorUnavailable, Err: errors.New("server unavailable")}, kind: FailureUnavailable, class: application.ErrorUnavailable},
		{name: "protocol", err: &mcp.Error{Class: mcp.ErrorProtocol, Err: errors.New("invalid response")}, kind: FailureProtocol, class: application.ErrorInvalidOutput},
		{name: "business", err: &mcp.Error{Class: mcp.ErrorBusiness, Err: errors.New("rejected")}, kind: FailureBusiness, class: application.ErrorBusiness},
		{name: "invalid output", err: &InvocationFailure{Kind: FailureInvalidOutput, Err: errors.New("empty response")}, kind: FailureInvalidOutput, class: application.ErrorInvalidOutput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := normalizeInvocationError(test.err)
			var failure *InvocationFailure
			if !errors.As(err, &failure) {
				t.Fatalf("normalized error %T does not expose InvocationFailure", err)
			}
			if failure.Kind != test.kind || failure.ApplicationClass() != test.class {
				t.Fatalf("failure = (%q, %q), want (%q, %q)", failure.Kind, failure.ApplicationClass(), test.kind, test.class)
			}
			if !errors.Is(err, test.err) {
				t.Fatalf("normalized error does not unwrap original error: %v", err)
			}
		})
	}
}

func TestRegistryNormalizesHTTPFailures(t *testing.T) {
	tests := []struct {
		name      string
		handler   http.HandlerFunc
		timeout   time.Duration
		wantKind  FailureKind
		wantClass application.ErrorClass
	}{
		{
			name: "timeout",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(50 * time.Millisecond)
				_, _ = w.Write([]byte("late"))
			},
			timeout: 5 * time.Millisecond, wantKind: FailureTimeout, wantClass: application.ErrorTimeout,
		},
		{
			name: "unavailable",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			},
			timeout: time.Second, wantKind: FailureUnavailable, wantClass: application.ErrorUnavailable,
		},
		{
			name: "business",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "rejected", http.StatusBadRequest)
			},
			timeout: time.Second, wantKind: FailureBusiness, wantClass: application.ErrorBusiness,
		},
		{
			name:    "invalid output",
			handler: func(http.ResponseWriter, *http.Request) {},
			timeout: time.Second, wantKind: FailureInvalidOutput, wantClass: application.ErrorInvalidOutput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			registry, err := NewRegistry(examplebusiness.ReadOnlyToolHTTP, server.URL, test.timeout)
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry.Execute(context.Background(), examplebusiness.ReadOnlyToolID, map[string]string{"message": "query"}, "")
			assertFailure(t, err, test.wantKind, test.wantClass)
		})
	}
}

func TestRegistryNormalizesLocalAndMCPFailures(t *testing.T) {
	t.Run("local business failure", func(t *testing.T) {
		registry, err := NewRegistry("", "", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, err = registry.Execute(context.Background(), examplebusiness.SideEffectToolID, map[string]string{"message": "touch"}, "")
		assertFailure(t, err, FailureBusiness, application.ErrorBusiness)
	})

	for _, test := range []struct {
		name      string
		body      string
		status    int
		wantKind  FailureKind
		wantClass application.ErrorClass
	}{
		{name: "protocol", body: "not-json", status: http.StatusOK, wantKind: FailureProtocol, wantClass: application.ErrorInvalidOutput},
		{name: "business", body: `{"jsonrpc":"2.0","id":"user_query","error":{"code":-1,"message":"rejected"}}`, status: http.StatusOK, wantKind: FailureBusiness, wantClass: application.ErrorBusiness},
		{name: "unavailable", body: "unavailable", status: http.StatusServiceUnavailable, wantKind: FailureUnavailable, wantClass: application.ErrorUnavailable},
	} {
		t.Run("mcp "+test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := mcp.NewClient(map[string]mcp.Server{"approved": {
				Endpoint: server.URL, AllowedTools: []string{examplebusiness.ReadOnlyToolID},
			}}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewRegistryWithMCP(examplebusiness.ReadOnlyToolMCP, "", time.Second, client, "approved")
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry.Execute(context.Background(), examplebusiness.ReadOnlyToolID, map[string]string{"message": "query"}, "")
			assertFailure(t, err, test.wantKind, test.wantClass)
		})
	}

	t.Run("mcp timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(50 * time.Millisecond)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"user_query","result":{"content":[{"type":"text","text":"late"}]}}`))
		}))
		defer server.Close()
		client, err := mcp.NewClient(map[string]mcp.Server{"approved": {
			Endpoint: server.URL, AllowedTools: []string{examplebusiness.ReadOnlyToolID},
		}}, 5*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := NewRegistryWithMCP(examplebusiness.ReadOnlyToolMCP, "", 5*time.Millisecond, client, "approved")
		if err != nil {
			t.Fatal(err)
		}
		_, err = registry.Execute(context.Background(), examplebusiness.ReadOnlyToolID, map[string]string{"message": "query"}, "")
		assertFailure(t, err, FailureTimeout, application.ErrorTimeout)
	})
}

func assertFailure(t *testing.T, err error, wantKind FailureKind, wantClass application.ErrorClass) {
	t.Helper()
	var failure *InvocationFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %T %v, want *InvocationFailure", err, err)
	}
	if failure.Kind != wantKind || failure.ApplicationClass() != wantClass {
		t.Fatalf("failure = (%q, %q), want (%q, %q): %v", failure.Kind, failure.ApplicationClass(), wantKind, wantClass, err)
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("failure message is empty")
	}
}
