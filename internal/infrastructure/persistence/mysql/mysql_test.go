package mysql

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
)

func TestDecodeQueryRequiresExactlyOnePositiveSelector(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "user", body: `{"user_id":1}`, want: true},
		{name: "order", body: `{"order_id":2}`, want: true},
		{name: "both", body: `{"user_id":1,"order_id":2}`},
		{name: "none", body: `{}`},
		{name: "zero", body: `{"user_id":0}`},
		{name: "unknown", body: `{"user_id":1,"extra":true}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeQuery([]byte(test.body))
			if (err == nil) != test.want {
				t.Fatalf("decodeQuery(%s) error=%v wantSuccess=%v", test.body, err, test.want)
			}
		})
	}
}

func TestDecodeInsertRejectsInvalidAmountsAndTrailingData(t *testing.T) {
	valid := `{"user_id":1,"product_id":1,"quantity":2,"total_amount":"39.80"}`
	if _, err := decodeInsert([]byte(valid)); err != nil {
		t.Fatalf("valid insert rejected: %v", err)
	}
	for _, body := range []string{
		`{"user_id":1,"product_id":1,"quantity":0,"total_amount":"1.00"}`,
		`{"user_id":1,"product_id":1,"quantity":1,"total_amount":"1.001"}`,
		`{"user_id":1,"product_id":1,"quantity":1,"total_amount":"-1.00"}`,
		valid + " {}",
	} {
		if _, err := decodeInsert([]byte(body)); err == nil {
			t.Fatalf("invalid insert accepted: %s", body)
		}
	}
}

func TestDecodeInputsRejectControlAndOversizedPayloads(t *testing.T) {
	control := `{"user_id":1,"product_id":1,"quantity":1,"total_amount":"1.00","note":"` + string([]byte{0x01}) + `"}`
	if _, err := decodeInsert([]byte(control)); err == nil {
		t.Fatal("control character payload was accepted")
	}
	oversized := bytes.Repeat([]byte("x"), maxPayloadBytes+1)
	if _, err := decodeQuery(oversized); err == nil {
		t.Fatal("oversized query payload was accepted")
	}
	if _, err := decodeInsert(oversized); err == nil {
		t.Fatal("oversized insert payload was accepted")
	}
}

func TestNormalizeAmount(t *testing.T) {
	for input, want := range map[string]string{"1": "1.00", "1.2": "1.20", "1.20": "1.20"} {
		if got := normalizeAmount(input); got != want {
			t.Fatalf("normalizeAmount(%q)=%q want %q", input, got, want)
		}
	}
	if strings.Contains(normalizeAmount("0.00"), "-") {
		t.Fatal("normalized amount must not be negative")
	}
}

func TestMySQLRunLeaseContract(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN 未设置；真实 MySQL lease 恢复测试未在当前环境运行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, err := Open(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	adapter := connection.NewRunLeaseAdapter()
	if err := adapter.AutoMigrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	runID := "contract-lease-" + now.Format("20060102150405.000000000")
	first := application.RunLease{RunID: runID, OwnerToken: "owner-1", ExpiresAt: now.Add(500 * time.Millisecond)}
	second := application.RunLease{RunID: runID, OwnerToken: "owner-2", ExpiresAt: now.Add(time.Second)}
	claimed, err := adapter.Claim(ctx, first, now)
	if err != nil || !claimed {
		t.Fatalf("first claim=%v err=%v", claimed, err)
	}
	claimed, err = adapter.Claim(ctx, second, now)
	if err != nil || claimed {
		t.Fatalf("live competing claim=%v err=%v", claimed, err)
	}
	claimed, err = adapter.Claim(ctx, second, first.ExpiresAt)
	if err != nil || !claimed {
		t.Fatalf("expired takeover=%v err=%v", claimed, err)
	}
	if owned, err := adapter.Owns(ctx, first, first.ExpiresAt); err != nil || owned {
		t.Fatalf("old owner owns expired lease=%v err=%v", owned, err)
	}
	if err := adapter.Release(ctx, second); err != nil {
		t.Fatal(err)
	}
}
