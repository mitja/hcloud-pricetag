package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An httptest stand-in for the Hetzner API; nothing here reaches the network.
func TestClientPricingAndServerRoundTrip(t *testing.T) {
	var put map[string]map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/pricing":
			io.WriteString(w, `{"pricing":{"currency":"EUR","server_types":[{"name":"cpx52","prices":[
				{"location":"fsn1","price_hourly":{"net":"0.1610000000","gross":"0.1916"},"price_monthly":{"net":"100.4900000000","gross":"119.58"}}]}]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/servers/7":
			io.WriteString(w, `{"server":{"id":7,"name":"web","created":"2025-01-01T00:00:00Z","labels":{"env":"prod"},
				"location":{"name":"fsn1"},"server_type":{"name":"cpx52"}}}`)
		case r.Method == http.MethodPut && r.URL.Path == "/servers/7":
			if err := json.NewDecoder(r.Body).Decode(&put); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-token")
	ctx := context.Background()

	p, err := c.Pricing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lp, ok := p.ListPrice("CPX52", "fsn1")
	if p.Currency != "EUR" || !ok || lp.MonthlyNetCents != 10049 || lp.HourlyNetMilliCents != 16100 {
		t.Fatalf("pricing: %+v %+v", p, lp)
	}

	s, err := c.GetServer(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if s.Type != "cpx52" || s.Location != "fsn1" || s.Labels["env"] != "prod" {
		t.Fatalf("server: %+v", s)
	}
	if err := c.SetLabels(ctx, 7, map[string]string{"env": "prod", testKeys.Monthly: "534"}); err != nil {
		t.Fatal(err)
	}
	if put["labels"][testKeys.Monthly] != "534" || put["labels"]["env"] != "prod" {
		t.Fatalf("put: %+v", put)
	}
}
