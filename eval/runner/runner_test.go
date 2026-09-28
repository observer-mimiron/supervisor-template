package runner

import "testing"

func TestCaptureResultProjectsOnlyExpectedFields(t *testing.T) {
	evidence := Evidence{Events: []Event{{Type: "text", Data: map[string]string{"content": `{"count":4,"customer_ids":["cust-001"],"secret":"drop"}`}}}}
	captureResult(&evidence, map[string]any{"count": 4, "customer_ids": []any{"cust-001"}})
	if len(evidence.Result) != 2 || evidence.Result["count"] != float64(4) {
		t.Fatalf("projected result=%#v", evidence.Result)
	}
	if _, ok := evidence.Result["secret"]; ok {
		t.Fatal("raw undeclared result field was retained")
	}
}

func TestTokenForSubjectUsesDistinctSyntheticCredential(t *testing.T) {
	if got := tokenForSubject("demo-user", "demo-token"); got != "demo-token" {
		t.Fatalf("default token=%q", got)
	}
	if got := tokenForSubject("demo-user-2", "demo-token"); got != "demo-token-demo-user-2" {
		t.Fatalf("variant token=%q", got)
	}
}

func TestMergeEventsUsesTerminalStepAsFailureReference(t *testing.T) {
	evidence := Evidence{}
	mergeEvents(&evidence, []Event{
		{Type: "progress", Data: map[string]string{"step_id": "run-1:step-1", "status": "succeeded"}},
		{Type: "failed", Data: map[string]string{"step_id": "run-1:step-2"}},
	}, map[string]bool{})
	if evidence.FailedStepID != "run-1:step-2" {
		t.Fatalf("failed step reference=%q", evidence.FailedStepID)
	}
}
