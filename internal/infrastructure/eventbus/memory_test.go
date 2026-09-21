package eventbus

import (
	"testing"
)

func TestMemoryBusAssignsSequenceAndRejectsSecondTerminal(t *testing.T) {
	bus := NewMemoryBus()
	for index, eventType := range []EventType{Started, Progress, Completed} {
		if _, err := bus.Append(RunEvent{EventID: string(rune('a' + index)), RunID: "run-1", Type: eventType}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bus.Append(RunEvent{EventID: "d", RunID: "run-1", Type: Failed}); err == nil {
		t.Fatal("expected duplicate terminal rejection")
	}
	if events := bus.Events("run-1"); len(events) != 3 || events[0].Sequence != 1 || events[2].Sequence != 3 {
		t.Fatalf("unexpected events: %#v", events)
	}
}
