package examplebusiness

import "testing"

func TestRegistrationReturnsDetachedDescriptors(t *testing.T) {
	workers := Workers()
	workers[0].AllowedTools[0] = "tampered"
	if Workers()[0].AllowedTools[0] != ReadOnlyToolID {
		t.Fatal("worker registration leaked mutable slice state")
	}
	routes := Routes()
	routes[0].Matches[0] = "tampered"
	if Routes()[0].Matches[0] != "分析" {
		t.Fatal("route registration leaked mutable slice state")
	}
}

func TestRegistrationRejectsUnknownBusinessReferences(t *testing.T) {
	if _, ok := WorkerFor("missing_worker"); ok {
		t.Fatal("unknown worker was registered")
	}
	if _, ok := RouteFor("missing_route"); ok {
		t.Fatal("unknown route was registered")
	}
	if ToolImplementationRegistered("missing_tool", ReadOnlyToolFake) {
		t.Fatal("unknown tool was registered")
	}
}
