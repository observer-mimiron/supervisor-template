package mysql

import (
	"bytes"
	"strings"
	"testing"
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
