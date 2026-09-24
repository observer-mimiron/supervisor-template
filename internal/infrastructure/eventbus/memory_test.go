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

func TestMemoryBusMakesDuplicateEventAppendIdempotent(t *testing.T) {
	bus := NewMemoryBus()
	event := RunEvent{EventID: "same", RunID: "run-idempotent", Type: Started}
	first, err := bus.Append(event)
	if err != nil {
		t.Fatal(err)
	}
	second, err := bus.Append(event)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != 1 || len(bus.Events(event.RunID)) != 1 {
		t.Fatalf("duplicate append was not idempotent: first=%#v second=%#v events=%#v", first, second, bus.Events(event.RunID))
	}
}

func TestMemoryBusReturnsDataClone(t *testing.T) {
	bus := NewMemoryBus()
	if _, err := bus.Append(RunEvent{EventID: "event-data", RunID: "run-data", Type: Started, Data: map[string]string{"value": "original"}}); err != nil {
		t.Fatal(err)
	}
	events := bus.Events("run-data")
	events[0].Data["value"] = "tampered"
	if got := bus.Events("run-data")[0].Data["value"]; got != "original" {
		t.Fatalf("event data leaked mutable state: %q", got)
	}
}
