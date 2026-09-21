package eino

import (
	"context"
	"testing"
)

func TestMemoryCheckpointStoreCopiesBytes(t *testing.T) {
	store := NewMemoryCheckpointStore().(*MemoryCheckpointStore)
	value := []byte("checkpoint")
	if err := store.Set(context.Background(), "run-1", value); err != nil {
		t.Fatal(err)
	}
	value[0] = 'X'
	got, ok, err := store.Get(context.Background(), "run-1")
	if err != nil || !ok || string(got) != "checkpoint" {
		t.Fatalf("got=%q ok=%v err=%v", got, ok, err)
	}
}
