package examplebusiness

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"
)

const FixtureAsOf = "2026-09-25"

type Customer struct {
	CustomerID     string    `json:"customer_id"`
	LastOrderAt    time.Time `json:"last_order_at"`
	Spend365d      int       `json:"spend_365d"`
	LastTouchAt    time.Time `json:"last_touch_at"`
	ContactConsent bool      `json:"contact_consent"`
}

type Audience struct {
	Count          int      `json:"count"`
	CustomerIDs    []string `json:"customer_ids"`
	Spend365dTotal int      `json:"spend_365d_total"`
}

type fixtureQuery struct {
	AsOf string `json:"as_of"`
}

var customers = []Customer{
	{CustomerID: "cust-001", LastOrderAt: date("2026-08-25"), Spend365d: 1200, LastTouchAt: date("2026-09-17"), ContactConsent: true},
	{CustomerID: "cust-002", LastOrderAt: date("2026-08-24"), Spend365d: 1000, LastTouchAt: date("2026-09-18"), ContactConsent: true},
	{CustomerID: "cust-003", LastOrderAt: date("2026-08-23"), Spend365d: 999, LastTouchAt: date("2026-09-17"), ContactConsent: true},
	{CustomerID: "cust-004", LastOrderAt: date("2026-08-22"), Spend365d: 1500, LastTouchAt: date("2026-09-18"), ContactConsent: false},
	{CustomerID: "cust-005", LastOrderAt: date("2026-08-26"), Spend365d: 2000, LastTouchAt: date("2026-09-17"), ContactConsent: true},
	{CustomerID: "cust-006", LastOrderAt: date("2026-08-20"), Spend365d: 2000, LastTouchAt: time.Time{}, ContactConsent: true},
	{CustomerID: "cust-007", LastOrderAt: date("2026-08-20"), Spend365d: 2000, LastTouchAt: date("2026-09-19"), ContactConsent: true},
	{CustomerID: "cust-008", LastOrderAt: date("2026-08-20"), Spend365d: 2000, LastTouchAt: date("2026-09-17"), ContactConsent: true},
}

func date(value string) time.Time {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		panic(err)
	}
	return parsed
}

// QueryAudience accepts only the fixed-date JSON contract and returns the synthetic audience.
func QueryAudience(input []byte) (Audience, error) {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	var query fixtureQuery
	if err := decoder.Decode(&query); err != nil {
		return Audience{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Audience{}, errors.New("查询包含尾随数据")
	}
	asOf, err := time.Parse("2006-01-02", query.AsOf)
	if err != nil || asOf.Format("2006-01-02") != FixtureAsOf {
		return Audience{}, errors.New("as_of 必须为固定日期 " + FixtureAsOf)
	}
	cutoffOrder, cutoffTouch := asOf.AddDate(0, 0, -30), asOf.AddDate(0, 0, -7)
	result := Audience{CustomerIDs: make([]string, 0)}
	for _, customer := range customers {
		// Missing last_touch_at means never contacted; "at least 7 days" includes the boundary.
		if customer.LastOrderAt.Before(cutoffOrder) && customer.Spend365d >= 1000 &&
			(customer.LastTouchAt.IsZero() || !customer.LastTouchAt.After(cutoffTouch)) && customer.ContactConsent {
			result.CustomerIDs = append(result.CustomerIDs, customer.CustomerID)
			result.Spend365dTotal += customer.Spend365d
		}
	}
	sort.Strings(result.CustomerIDs)
	result.Count = len(result.CustomerIDs)
	return result, nil
}
