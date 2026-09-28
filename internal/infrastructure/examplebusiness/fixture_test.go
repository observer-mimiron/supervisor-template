package examplebusiness

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestQueryAudience(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Audience
		wantErr bool
	}{
		{name: "fixed audience", input: `{"as_of":"2026-09-25"}`, want: Audience{Count: 4, CustomerIDs: []string{"cust-001", "cust-002", "cust-006", "cust-008"}, Spend365dTotal: 6200}},
		{name: "missing touch date qualifies", input: `{"as_of":"2026-09-25"}`, want: Audience{Count: 4, CustomerIDs: []string{"cust-001", "cust-002", "cust-006", "cust-008"}, Spend365dTotal: 6200}},
		{name: "reject extra field", input: `{"as_of":"2026-09-25","name":"A"}`, wantErr: true},
		{name: "reject contact field", input: `{"as_of":"2026-09-25","phone":"555"}`, wantErr: true},
		{name: "reject credentials field", input: `{"as_of":"2026-09-25","api_key":"secret"}`, wantErr: true},
		{name: "reject natural language", input: `find dormant customers`, wantErr: true},
		{name: "reject alternate date", input: `{"as_of":"2026-09-26"}`, wantErr: true},
		{name: "reject trailing JSON", input: `{"as_of":"2026-09-25"}{}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := QueryAudience([]byte(test.input))
			if (err != nil) != test.wantErr {
				t.Fatalf("QueryAudience() error = %v, wantErr %v", err, test.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("QueryAudience() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestAudienceJSONContract(t *testing.T) {
	got, err := QueryAudience([]byte(`{"as_of":"2026-09-25"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"count":4,"customer_ids":["cust-001","cust-002","cust-006","cust-008"],"spend_365d_total":6200}`
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != want {
		t.Fatalf("JSON = %s, want %s", encoded, want)
	}
}

func TestQueryAudienceBoundaryPredicates(t *testing.T) {
	tests := []struct {
		name      string
		customer  Customer
		wantMatch bool
	}{
		{
			name:     "exactly 30 days since order is excluded",
			customer: Customer{CustomerID: "boundary-order", LastOrderAt: date("2026-08-26"), Spend365d: 1000, LastTouchAt: date("2026-09-17"), ContactConsent: true},
		},
		{
			name:      "exactly 1000 spend qualifies",
			customer:  Customer{CustomerID: "boundary-spend", LastOrderAt: date("2026-08-25"), Spend365d: 1000, LastTouchAt: date("2026-09-17"), ContactConsent: true},
			wantMatch: true,
		},
		{
			name:      "exactly 7 days since touch qualifies",
			customer:  Customer{CustomerID: "boundary-touch", LastOrderAt: date("2026-08-25"), Spend365d: 1000, LastTouchAt: date("2026-09-18"), ContactConsent: true},
			wantMatch: true,
		},
		{
			name:      "missing touch qualifies",
			customer:  Customer{CustomerID: "boundary-missing-touch", LastOrderAt: date("2026-08-25"), Spend365d: 1000, ContactConsent: true},
			wantMatch: true,
		},
		{
			name:     "consent false is excluded",
			customer: Customer{CustomerID: "boundary-no-consent", LastOrderAt: date("2026-08-25"), Spend365d: 1000, LastTouchAt: time.Time{}, ContactConsent: false},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := customers
			customers = append(append([]Customer(nil), customers...), test.customer)
			defer func() { customers = original }()

			got, err := QueryAudience([]byte(`{"as_of":"2026-09-25"}`))
			if err != nil {
				t.Fatal(err)
			}
			matched := containsCustomerID(got.CustomerIDs, test.customer.CustomerID)
			if matched != test.wantMatch {
				t.Fatalf("customer %q matched = %v, want %v; audience = %#v", test.customer.CustomerID, matched, test.wantMatch, got)
			}
		})
	}
}

func containsCustomerID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
