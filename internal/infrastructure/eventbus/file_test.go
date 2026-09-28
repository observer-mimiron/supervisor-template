package eventbus

import (
	"os"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

func TestFileBusReplaysAndDeduplicatesStableEvents(t *testing.T) {
	bus, err := NewFileBus(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	event := agent.RunEvent{EventID: "event-1", RunID: "run-file", Type: agent.Started, Data: map[string]string{"status": "started"}}
	if _, err := bus.Append(event); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.Append(event); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewFileBus(bus.dir)
	if err != nil {
		t.Fatal(err)
	}
	events := restarted.Events(event.RunID)
	if len(events) != 1 || events[0].Sequence != 1 || events[0].Data["status"] != "started" {
		t.Fatalf("unexpected replay: %#v", events)
	}
}

func TestFileBusRejectsUnsupportedVersion(t *testing.T) {
	bus, err := NewFileBus(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bus.path("run-version"), []byte(`{"schema_version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.Append(agent.RunEvent{EventID: "event-1", RunID: "run-version", Type: agent.Started}); err == nil {
		t.Fatal("expected unsupported event schema rejection")
	}
}
