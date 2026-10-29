package main

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"
)

// fakeAPI is an in-memory Hetzner project. No test talks to the real API.
type fakeAPI struct {
	servers map[int64]Server
	pricing Pricing
	writes  []fakeWrite
	// onGet, when set, mutates a server between listing and the re-read
	// before a write, to simulate a concurrent change.
	onGet func(*Server)
}

type fakeWrite struct {
	id     int64
	labels map[string]string
}

func (f *fakeAPI) ListServers(context.Context) ([]Server, error) {
	var out []Server
	for _, s := range f.servers {
		out = append(out, cloneServer(s))
	}
	return out, nil
}

func (f *fakeAPI) GetServer(_ context.Context, id int64) (Server, error) {
	s := f.servers[id]
	if f.onGet != nil {
		f.onGet(&s)
		f.servers[id] = s
	}
	return cloneServer(s), nil
}

func (f *fakeAPI) Pricing(context.Context) (Pricing, error) { return f.pricing, nil }

func (f *fakeAPI) SetLabels(_ context.Context, id int64, labels map[string]string) error {
	f.writes = append(f.writes, fakeWrite{id, labels})
	s := f.servers[id]
	s.Labels = labels
	f.servers[id] = s
	return nil
}

func cloneServer(s Server) Server {
	l := map[string]string{}
	for k, v := range s.Labels {
		l[k] = v
	}
	s.Labels = l
	return s
}

func newFake(currency string, servers ...Server) *fakeAPI {
	f := &fakeAPI{
		servers: map[int64]Server{},
		pricing: Pricing{Currency: currency, ServerTypes: map[string]map[string]Price{
			"cpx52": {"fsn1": {MonthlyNetCents: 10049, HourlyNetMilliCents: 16100}},
		}},
	}
	for _, s := range servers {
		f.servers[s.ID] = s
	}
	return f
}

func runInteractive(t *testing.T, f *fakeAPI, stdin string) string {
	t.Helper()
	var out strings.Builder
	it := &Interactive{API: f, Keys: testKeys, In: bufio.NewReader(strings.NewReader(stdin)), Out: &out}
	if err := it.Run(context.Background()); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	return out.String()
}

var created = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func TestInteractiveSetsBothPricesAndKeepsForeignLabels(t *testing.T) {
	f := newFake("EUR", srv(42, "web-1", "cpx52", "fsn1", created, map[string]string{"env": "prod"}))
	// pick #1, monthly, hourly, default currency, confirm, then EOF.
	out := runInteractive(t, f, "1\n36.49\n0.0585\n\ny\n")

	if len(f.writes) != 1 {
		t.Fatalf("writes: %+v\n%s", f.writes, out)
	}
	got := f.writes[0].labels
	want := map[string]string{
		"env":             "prod",
		testKeys.Monthly:  "3649",
		testKeys.Hourly:   "5850",
		testKeys.Currency: "EUR",
	}
	if len(got) != len(want) {
		t.Fatalf("labels: %+v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// The list price is shown for comparison.
	if !strings.Contains(out, "100.49/mo") || !strings.Contains(out, "0.1610/h") {
		t.Errorf("list price not shown:\n%s", out)
	}
}

func TestInteractiveShowsCurrentLabels(t *testing.T) {
	f := newFake("EUR", srv(42, "web-1", "cpx52", "fsn1", created, map[string]string{
		testKeys.Monthly: "3649", testKeys.Hourly: "5850", testKeys.Currency: "EUR",
	}))
	out := runInteractive(t, f, "42\n\n\n\n")
	if !strings.Contains(out, "36.49/mo 0.0585/h EUR") {
		t.Errorf("current override not shown:\n%s", out)
	}
	if !strings.Contains(out, `monthly "3649"`) {
		t.Errorf("raw label values not shown:\n%s", out)
	}
	if !strings.Contains(out, "nothing to change") || len(f.writes) != 0 {
		t.Errorf("keeping everything must not write: %+v\n%s", f.writes, out)
	}
}

// Clearing removes the label; it is not the same as a price of zero.
func TestInteractiveClearDiffersFromZero(t *testing.T) {
	labelled := map[string]string{
		"env": "prod", testKeys.Monthly: "3649", testKeys.Hourly: "5850", testKeys.Currency: "EUR",
	}

	f := newFake("EUR", srv(1, "a", "cpx52", "fsn1", created, labelled))
	runInteractive(t, f, "1\n0\n\n\ny\n")
	if l := f.writes[0].labels; l[testKeys.Monthly] != "0" || l[testKeys.Hourly] != "5850" || l[testKeys.Currency] != "EUR" {
		t.Fatalf("zero: %+v", l)
	}

	f = newFake("EUR", srv(1, "a", "cpx52", "fsn1", created, labelled))
	runInteractive(t, f, "1\n-\n\n\ny\n")
	l := f.writes[0].labels
	if _, ok := l[testKeys.Monthly]; ok {
		t.Fatalf("cleared monthly still present: %+v", l)
	}
	if l[testKeys.Hourly] != "5850" || l[testKeys.Currency] != "EUR" {
		t.Fatalf("clearing one dimension must leave the other: %+v", l)
	}
}

func TestInteractiveClearingBothRemovesCurrencyToo(t *testing.T) {
	f := newFake("EUR", srv(1, "a", "cpx52", "fsn1", created, map[string]string{
		"env": "prod", testKeys.Monthly: "3649", testKeys.Hourly: "5850", testKeys.Currency: "EUR",
	}))
	// No currency prompt when no amount remains.
	runInteractive(t, f, "1\n-\n-\ny\n")
	if l := f.writes[0].labels; len(l) != 1 || l["env"] != "prod" {
		t.Fatalf("labels: %+v", l)
	}
}

func TestInteractiveDeclineWritesNothing(t *testing.T) {
	f := newFake("EUR", srv(1, "a", "cpx52", "fsn1", created, nil))
	out := runInteractive(t, f, "1\n5.34\n\n\nn\n")
	if len(f.writes) != 0 {
		t.Fatalf("declined but wrote: %+v", f.writes)
	}
	if !strings.Contains(out, "not written") {
		t.Errorf("output:\n%s", out)
	}
}

func TestInteractiveRepromptsOnBadInput(t *testing.T) {
	f := newFake("EUR", srv(1, "a", "cpx52", "fsn1", created, nil))
	// 7 is no server; 5.345 needs rounding; 5,34 uses a comma; USD differs
	// from the account; GBP is no currency.
	out := runInteractive(t, f, "7\n1\n5.345\n5,34\n5.34\n0.00725\nGBP\nUSD\neur\ny\n")
	if len(f.writes) != 1 {
		t.Fatalf("writes: %+v\n%s", f.writes, out)
	}
	l := f.writes[0].labels
	if l[testKeys.Monthly] != "534" || l[testKeys.Hourly] != "725" || l[testKeys.Currency] != "EUR" {
		t.Fatalf("labels: %+v", l)
	}
	for _, msg := range []string{"no server 7", "rounded", "dot", "must be EUR or USD", "billed in EUR"} {
		if !strings.Contains(out, msg) {
			t.Errorf("missing %q in:\n%s", msg, out)
		}
	}
}

func TestInteractiveUSDAccount(t *testing.T) {
	f := newFake("USD", srv(1, "a", "cpx52", "fsn1", created, nil))
	runInteractive(t, f, "1\n\n0.0073\n\ny\n")
	l := f.writes[0].labels
	if l[testKeys.Currency] != "USD" || l[testKeys.Hourly] != "730" {
		t.Fatalf("labels: %+v", l)
	}
	if _, ok := l[testKeys.Monthly]; ok {
		t.Fatalf("monthly was left empty and must not be set: %+v", l)
	}
}

// PUT replaces the whole label map; a label added between listing and writing
// must survive.
func TestInteractiveWritesOntoFreshLabels(t *testing.T) {
	f := newFake("EUR", srv(1, "a", "cpx52", "fsn1", created, map[string]string{"env": "prod"}))
	f.onGet = func(s *Server) { s.Labels["owner"] = "team-b" }
	runInteractive(t, f, "1\n5.34\n\n\ny\n")
	l := f.writes[0].labels
	if l["owner"] != "team-b" || l["env"] != "prod" || l[testKeys.Monthly] != "534" {
		t.Fatalf("labels: %+v", l)
	}
}

func TestInteractiveRefusesWhenServerChangedType(t *testing.T) {
	f := newFake("EUR", srv(1, "a", "cpx52", "fsn1", created, nil))
	f.onGet = func(s *Server) { s.Type = "cpx62" }
	out := runInteractive(t, f, "1\n5.34\n\n\ny\n")
	if len(f.writes) != 0 {
		t.Fatalf("wrote onto a rescaled server: %+v", f.writes)
	}
	if !strings.Contains(out, "not written: server is now cpx62") {
		t.Errorf("output:\n%s", out)
	}
}

func TestInteractiveQuit(t *testing.T) {
	f := newFake("EUR", srv(1, "a", "cpx52", "fsn1", created, nil))
	runInteractive(t, f, "q\n")
	runInteractive(t, f, "1\n5.34\nq\n")
	runInteractive(t, f, "")
	if len(f.writes) != 0 {
		t.Fatalf("writes: %+v", f.writes)
	}
}

func TestInteractiveUsesConfiguredNamespace(t *testing.T) {
	keys, err := KeysFor("example.org")
	if err != nil {
		t.Fatal(err)
	}
	f := newFake("EUR", srv(1, "a", "cpx52", "fsn1", created, map[string]string{
		testKeys.Monthly: "1", // another namespace's label: not ours, kept
	}))
	var out strings.Builder
	it := &Interactive{API: f, Keys: keys, In: bufio.NewReader(strings.NewReader("1\n5.34\n\n\ny\n")), Out: &out}
	if err := it.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	l := f.writes[0].labels
	if l["example.org/monthly-net-cents"] != "534" || l["example.org/price-currency"] != "EUR" || l[testKeys.Monthly] != "1" {
		t.Fatalf("labels: %+v", l)
	}
}
