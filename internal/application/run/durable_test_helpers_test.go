package run

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
)

type durableTestClock struct {
	now time.Time
}

func newDurableTestClock(now time.Time) *durableTestClock {
	return &durableTestClock{now: now}
}

func (c *durableTestClock) Now() time.Time { return c.now }

func (c *durableTestClock) Advance(delta time.Duration) { c.now = c.now.Add(delta) }

func newTestOwnerToken(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw[:])
}

func newTestLease(t *testing.T, runID string, now time.Time, ttl time.Duration) application.RunLease {
	t.Helper()
	return application.RunLease{RunID: runID, OwnerToken: newTestOwnerToken(t), ExpiresAt: now.Add(ttl)}
}
