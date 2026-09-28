package run

import (
	"context"
	"errors"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/application"
)

func TestDefaultErrorPolicyClassifiesAndBoundsRetries(t *testing.T) {
	policy := DefaultErrorPolicy{}
	tests := []struct {
		name  string
		err   error
		phase application.ExecutionPhase
		want  application.ErrorClass
	}{
		{"pre-call only before invocation", &application.PreCallError{Err: errors.New("connection refused")}, application.PhasePreCall, application.ErrorPreCallFailure},
		{"pre-call claim after invocation is not retryable", &application.PreCallError{Err: errors.New("connection refused")}, application.PhaseInFlight, application.ErrorBusiness},
		{"deadline", context.DeadlineExceeded, application.PhaseInFlight, application.ErrorTimeout},
		{"cancel", context.Canceled, application.PhaseInFlight, application.ErrorCanceled},
		{"unknown dominates commit", application.ErrOutcomeUnknown, application.PhaseCommit, application.ErrorUnknownOutcome},
		{"commit uncertainty", errors.New("commit missing"), application.PhaseCommit, application.ErrorUnknownOutcome},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := policy.Classify(tt.err, tt.phase); got != tt.want {
				t.Fatalf("Classify() = %q, want %q", got, tt.want)
			}
		})
	}
	if policy.Decide(application.ErrorPreCallFailure, 1, 1) != application.Retry || policy.Decide(application.ErrorPreCallFailure, 2, 1) != application.Terminate {
		t.Fatal("pre-call retry limit not enforced")
	}
	if policy.Decide(application.ErrorUnknownOutcome, 1, 5) != application.Reconcile {
		t.Fatal("unknown outcome must require reconciliation")
	}
	for _, class := range []application.ErrorClass{application.ErrorTimeout, application.ErrorCanceled, application.ErrorInvalidOutput, application.ErrorPolicyDenied, application.ErrorBusiness, application.ErrorUnavailable, application.ErrorInternal} {
		if got := policy.Decide(class, 1, 5); got != application.Terminate {
			t.Fatalf("class %q decision = %q, want terminate", class, got)
		}
	}
}
