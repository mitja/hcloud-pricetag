package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

var cutoff = time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)

func testTable() *Table {
	return &Table{
		Source: "test", Currency: "EUR", Basis: "net", Effective: "2026-06-15T06:00:00Z",
		ServerTypes: []Entry{{
			Name: "cpx52", Locations: []string{"fsn1"},
			Old: Price{MonthlyNetCents: 3649, HourlyNetMilliCents: 5850},
			New: Price{MonthlyNetCents: 10049, HourlyNetMilliCents: 16100},
		}},
	}
}

func srv(id int64, name, typ, loc string, created time.Time, labels map[string]string) Server {
	if labels == nil {
		labels = map[string]string{}
	}
	return Server{ID: id, Name: name, Type: typ, Location: loc, Created: created, Labels: labels}
}

func TestBuildPlanProposesOnlyPreCutoffServersWithAKnownPrice(t *testing.T) {
	before := cutoff.Add(-24 * time.Hour)
	after := cutoff.Add(24 * time.Hour)
	p := BuildPlan([]Server{
		srv(1, "legacy", "cpx52", "fsn1", before, nil),
		srv(2, "fresh", "cpx52", "fsn1", after, nil),
		srv(3, "unknown-type", "cx22", "fsn1", before, nil),
		srv(4, "unknown-loc", "cpx52", "sin", before, nil),
	}, testTable(), cutoff, "EUR")

	if len(p.Servers) != 1 || p.Servers[0].ID != 1 {
		t.Fatalf("proposed: %+v", p.Servers)
	}
	if p.Servers[0].Confirmed {
		t.Error("a fresh plan must never arrive pre-confirmed")
	}
	if p.Servers[0].ProposedEURMonth != "36.49" || p.Servers[0].CurrentListEURMonth != "100.49" {
		t.Errorf("amounts: %+v", p.Servers[0])
	}
	if p.TotalInspected != 4 || len(p.Skipped) != 3 {
		t.Fatalf("skipped %d of %d", len(p.Skipped), p.TotalInspected)
	}
	// Every skip carries a reason: a silent skip reads as "covered everything".
	for _, s := range p.Skipped {
		if s.Reason == "" {
			t.Errorf("server %d skipped without a reason", s.ID)
		}
	}
}

// The adjustment applies from the cutoff onwards, so a server created at the
// exact instant is already on new pricing.
func TestServerCreatedExactlyAtCutoffIsNotProposed(t *testing.T) {
	p := BuildPlan([]Server{srv(1, "edge", "cpx52", "fsn1", cutoff, nil)}, testTable(), cutoff, "EUR")
	if len(p.Servers) != 0 {
		t.Fatalf("proposed: %+v", p.Servers)
	}
}

func TestAlreadyLabelledServerIsSkipped(t *testing.T) {
	before := cutoff.Add(-24 * time.Hour)
	p := BuildPlan([]Server{srv(1, "done", "cpx52", "fsn1", before, map[string]string{
		LabelMonthly:  "3649",
		LabelHourly:   "5850",
		LabelCurrency: "EUR",
	})}, testTable(), cutoff, "EUR")
	if len(p.Servers) != 0 {
		t.Fatalf("re-running must be a no-op, got %+v", p.Servers)
	}
	if len(p.Skipped) != 1 || !strings.Contains(p.Skipped[0].Reason, "already") {
		t.Fatalf("skipped: %+v", p.Skipped)
	}
}

func TestPartiallyLabelledServerIsStillProposed(t *testing.T) {
	before := cutoff.Add(-24 * time.Hour)
	p := BuildPlan([]Server{srv(1, "half", "cpx52", "fsn1", before, map[string]string{
		LabelMonthly: "3649", // hourly missing
	})}, testTable(), cutoff, "EUR")
	if len(p.Servers) != 1 {
		t.Fatalf("a half-written override must be completed, got %+v", p.Servers)
	}
}

// A rescale between plan and apply is exactly the event that forfeits the old
// price. It shows up as a changed server type.
func TestDriftDetectsRescaleRelocationAndIdentitySwap(t *testing.T) {
	before := cutoff.Add(-24 * time.Hour)
	e := PlanEntry{ID: 1, ServerType: "cpx52", Location: "fsn1", Created: before}

	if d := e.Drift(srv(1, "x", "cpx52", "fsn1", before, nil)); d != "" {
		t.Errorf("unchanged server should not drift: %s", d)
	}
	if d := e.Drift(srv(1, "x", "cpx62", "fsn1", before, nil)); !strings.Contains(d, "rescale") {
		t.Errorf("rescale not caught: %q", d)
	}
	if d := e.Drift(srv(1, "x", "cpx52", "nbg1", before, nil)); !strings.Contains(d, "location") {
		t.Errorf("relocation not caught: %q", d)
	}
	if d := e.Drift(srv(1, "x", "cpx52", "fsn1", before.Add(time.Hour), nil)); !strings.Contains(d, "not the same server") {
		t.Errorf("id reuse not caught: %q", d)
	}
}

// PUT /servers/{id} replaces the whole label map, so a merge bug would delete
// the customer's own labels and nobody would notice for weeks.
func TestMergePreservesForeignLabels(t *testing.T) {
	e := PlanEntry{Labels: map[string]string{LabelMonthly: "3649", LabelHourly: "5850", LabelCurrency: "EUR"}}
	merged := e.MergeLabels(map[string]string{
		"cost-center": "platform",
		"env":         "prod",
		LabelMonthly:  "9999", // stale value must be replaced
	})
	if merged["cost-center"] != "platform" || merged["env"] != "prod" {
		t.Fatalf("foreign labels lost: %+v", merged)
	}
	if merged[LabelMonthly] != "3649" || merged[LabelHourly] != "5850" {
		t.Fatalf("override not applied: %+v", merged)
	}
	if len(merged) != 5 {
		t.Fatalf("unexpected label count: %+v", merged)
	}
}

func TestStripOverridesKeepsEverythingElse(t *testing.T) {
	stripped, changed := StripOverrides(map[string]string{
		"cost-center": "platform",
		LabelMonthly:  "3649",
		LabelHourly:   "5850",
		LabelCurrency: "EUR",
	})
	if !changed {
		t.Fatal("changed should be true")
	}
	if len(stripped) != 1 || stripped["cost-center"] != "platform" {
		t.Fatalf("stripped: %+v", stripped)
	}
	if _, changed := StripOverrides(map[string]string{"env": "prod"}); changed {
		t.Error("nothing to strip should report unchanged")
	}
}

type fakeWriter struct {
	calls map[int64]map[string]string
	err   error
}

func (f *fakeWriter) SetLabels(_ context.Context, id int64, labels map[string]string) error {
	if f.err != nil {
		return f.err
	}
	if f.calls == nil {
		f.calls = map[int64]map[string]string{}
	}
	f.calls[id] = labels
	return nil
}

func TestApplyWritesOnlyConfirmedEntries(t *testing.T) {
	before := cutoff.Add(-24 * time.Hour)
	plan := &Plan{Servers: []PlanEntry{
		{ID: 1, Name: "yes", ServerType: "cpx52", Location: "fsn1", Created: before,
			Labels: map[string]string{LabelMonthly: "3649"}, Confirmed: true},
		{ID: 2, Name: "no", ServerType: "cpx52", Location: "fsn1", Created: before,
			Labels: map[string]string{LabelMonthly: "3649"}, Confirmed: false},
	}}
	live := []Server{
		srv(1, "yes", "cpx52", "fsn1", before, map[string]string{"env": "prod"}),
		srv(2, "no", "cpx52", "fsn1", before, nil),
	}
	w := &fakeWriter{}
	results, err := Apply(context.Background(), w, plan, live, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.calls) != 1 {
		t.Fatalf("wrote %d server(s), want 1: %+v", len(w.calls), w.calls)
	}
	if w.calls[1]["env"] != "prod" || w.calls[1][LabelMonthly] != "3649" {
		t.Fatalf("payload: %+v", w.calls[1])
	}
	var written, skipped int
	for _, r := range results {
		if r.Written {
			written++
		} else {
			skipped++
		}
	}
	if written != 1 || skipped != 1 {
		t.Fatalf("results: %d written, %d skipped", written, skipped)
	}
}

func TestApplyRefusesDriftedServer(t *testing.T) {
	before := cutoff.Add(-24 * time.Hour)
	plan := &Plan{Servers: []PlanEntry{{
		ID: 1, Name: "rescaled", ServerType: "cpx52", Location: "fsn1", Created: before,
		Labels: map[string]string{LabelMonthly: "3649"}, Confirmed: true,
	}}}
	// Confirmed by a human, then rescaled before apply ran.
	live := []Server{srv(1, "rescaled", "cpx62", "fsn1", before, nil)}

	w := &fakeWriter{}
	results, err := Apply(context.Background(), w, plan, live, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.calls) != 0 {
		t.Fatalf("a drifted server must not be written: %+v", w.calls)
	}
	if len(results) != 1 || results[0].Written || !strings.Contains(results[0].Skipped, "rescale") {
		t.Fatalf("results: %+v", results)
	}
}

func TestApplySkipsVanishedServer(t *testing.T) {
	plan := &Plan{Servers: []PlanEntry{{ID: 99, Name: "gone", Confirmed: true}}}
	w := &fakeWriter{}
	results, err := Apply(context.Background(), w, plan, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.calls) != 0 || !strings.Contains(results[0].Skipped, "no longer exists") {
		t.Fatalf("results: %+v", results)
	}
}

func TestApplyDryRunWritesNothing(t *testing.T) {
	before := cutoff.Add(-24 * time.Hour)
	plan := &Plan{Servers: []PlanEntry{{
		ID: 1, Name: "yes", ServerType: "cpx52", Location: "fsn1", Created: before,
		Labels: map[string]string{LabelMonthly: "3649"}, Confirmed: true,
	}}}
	live := []Server{srv(1, "yes", "cpx52", "fsn1", before, nil)}
	w := &fakeWriter{}
	results, err := Apply(context.Background(), w, plan, live, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.calls) != 0 {
		t.Fatalf("dry run wrote: %+v", w.calls)
	}
	if !results[0].Written {
		t.Error("dry run should still report what it would write")
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	before := cutoff.Add(-24 * time.Hour)
	plan := &Plan{Servers: []PlanEntry{{
		ID: 1, Name: "done", ServerType: "cpx52", Location: "fsn1", Created: before,
		Labels: map[string]string{LabelMonthly: "3649", LabelHourly: "5850", LabelCurrency: "EUR"}, Confirmed: true,
	}}}
	live := []Server{srv(1, "done", "cpx52", "fsn1", before, map[string]string{
		LabelMonthly: "3649", LabelHourly: "5850", LabelCurrency: "EUR",
	})}
	w := &fakeWriter{}
	results, _ := Apply(context.Background(), w, plan, live, false)
	if len(w.calls) != 0 {
		t.Fatalf("re-applying an unchanged override must not call the API: %+v", w.calls)
	}
	if results[0].Skipped != "already up to date" {
		t.Fatalf("results: %+v", results)
	}
}

func TestPlanRoundTripsThroughDisk(t *testing.T) {
	before := cutoff.Add(-24 * time.Hour)
	p := BuildPlan([]Server{srv(1, "legacy", "cpx52", "fsn1", before, nil)}, testTable(), cutoff, "EUR")
	path := t.TempDir() + "/plan.json"
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Servers) != 1 || loaded.Servers[0].ID != 1 {
		t.Fatalf("loaded: %+v", loaded.Servers)
	}
	if loaded.Servers[0].Labels[LabelMonthly] != "3649" {
		t.Fatalf("labels: %+v", loaded.Servers[0].Labels)
	}
	if !loaded.Cutoff.Equal(cutoff) {
		t.Errorf("cutoff: %s", loaded.Cutoff)
	}
	if len(loaded.Comment) == 0 {
		t.Error("the plan file must explain itself")
	}
}

// A server carrying an override without a currency is not "already labelled":
// an amount with no stated currency is not an amount, and the reader ignores
// it (specs/16 §2.3).
func TestOverrideWithoutCurrencyIsIncomplete(t *testing.T) {
	before := cutoff.Add(-24 * time.Hour)
	p := BuildPlan([]Server{srv(1, "legacy", "cpx52", "fsn1", before, map[string]string{
		LabelMonthly: "3649", LabelHourly: "5850", // currency missing
	})}, testTable(), cutoff, "EUR")
	if len(p.Servers) != 1 {
		t.Fatalf("a currency-less override must be completed, got %+v", p.Servers)
	}
	if p.Servers[0].Labels[LabelCurrency] != "EUR" {
		t.Fatalf("labels: %+v", p.Servers[0].Labels)
	}
}

func TestRevertRemovesTheCurrencyLabelToo(t *testing.T) {
	stripped, changed := StripOverrides(map[string]string{
		"env":         "prod",
		LabelMonthly:  "3649",
		LabelHourly:   "5850",
		LabelCurrency: "EUR",
	})
	if !changed || len(stripped) != 1 || stripped["env"] != "prod" {
		t.Fatalf("stripped: %+v", stripped)
	}
}
