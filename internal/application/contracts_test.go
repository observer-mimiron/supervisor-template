package application

import (
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

func TestErrorClassesMatchDomainContract(t *testing.T) {
	want := []ErrorClass{"pre_call_failure", "timeout", "canceled", "invalid_output", "policy_denied", "business_failure", "unavailable", "unknown_outcome", "internal"}
	got := []ErrorClass{ErrorPreCallFailure, ErrorTimeout, ErrorCanceled, ErrorInvalidOutput, ErrorPolicyDenied, ErrorBusiness, ErrorUnavailable, ErrorUnknownOutcome, ErrorInternal}
	domain := []agent.ErrorClass{agent.ClassPreCallFailure, agent.ClassTimeout, agent.ClassCanceled, agent.ClassInvalidOutput, agent.ClassPolicyDenied, agent.ClassBusiness, agent.ClassUnavailable, agent.ClassUnknownOutcome, agent.ClassInternal}
	for i := range want {
		if got[i] != want[i] || domain[i] != want[i] {
			t.Fatalf("error class %d = app:%q domain:%q, want %q", i, got[i], domain[i], want[i])
		}
	}
}
