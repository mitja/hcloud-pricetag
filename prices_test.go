package main

import (
	"strconv"
	"testing"
)

func TestEmbeddedTableLoadsAndValidates(t *testing.T) {
	table, err := LoadTable("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(table.ServerTypes) == 0 {
		t.Fatal("no entries")
	}
	if _, err := table.EffectiveTime(); err != nil {
		t.Fatalf("effective: %v", err)
	}
	if table.Basis != "net" {
		t.Fatalf("bundled table basis is %q, want net", table.Basis)
	}
}

// The published table prices a type differently per region. A lookup that
// ignored the location would be wrong by a factor of two on cpx52.
func TestLookupIsPerTypeAndLocation(t *testing.T) {
	table, err := LoadTable("")
	if err != nil {
		t.Fatal(err)
	}
	de, ok := table.Lookup("cpx52", "fsn1")
	if !ok {
		t.Fatal("cpx52@fsn1 missing")
	}
	sin, ok := table.Lookup("cpx52", "sin")
	if !ok {
		t.Fatal("cpx52@sin missing")
	}
	if de.Old.MonthlyNetCents != 3649 {
		t.Errorf("cpx52@fsn1 old monthly: got %d, want 3649", de.Old.MonthlyNetCents)
	}
	if sin.Old.MonthlyNetCents != 7799 {
		t.Errorf("cpx52@sin old monthly: got %d, want 7799", sin.Old.MonthlyNetCents)
	}
	if de.Old.MonthlyNetCents == sin.Old.MonthlyNetCents {
		t.Error("the two regions must not collapse to one price")
	}
}

func TestLookupIsCaseInsensitiveOnTypeName(t *testing.T) {
	table, _ := LoadTable("")
	if _, ok := table.Lookup("CPX52", "fsn1"); !ok {
		t.Error("uppercase type name should resolve")
	}
}

func TestLookupUnknownTypeOrLocation(t *testing.T) {
	table, _ := LoadTable("")
	if _, ok := table.Lookup("cx22", "fsn1"); ok {
		t.Error("cx22 is not in the published table — it must not resolve")
	}
	if _, ok := table.Lookup("cpx52", "nowhere"); ok {
		t.Error("unknown location must not resolve")
	}
}

// Spot-checks against a real GET /pricing capture from 2026-07-28 — after the
// adjustment, so its NET values are this table's "new" column. The published
// page quotes gross; these are what the API actually reports.
func TestSpotCheckAgainstCapturedAPIValues(t *testing.T) {
	table, _ := LoadTable("")
	cases := []struct {
		typ, loc                   string
		oldMonth, newMonth         int64
		oldHourMilli, newHourMilli int64
	}{
		{"cax11", "fsn1", 449, 599, 720, 960},
		{"ccx53", "fsn1", 24999, 53349, 40060, 85500},
		{"cpx52", "fsn1", 3649, 10049, 5850, 16100},
		{"cx23", "hel1", 399, 549, 640, 880},
		{"cpx11", "ash", 599, 1749, 960, 2800},
		{"ccx63", "sin", 62699, 103649, 100480, 166100},
	}
	for _, c := range cases {
		e, ok := table.Lookup(c.typ, c.loc)
		if !ok {
			t.Errorf("%s@%s missing", c.typ, c.loc)
			continue
		}
		if e.Old.MonthlyNetCents != c.oldMonth || e.New.MonthlyNetCents != c.newMonth {
			t.Errorf("%s@%s monthly: got %d/%d want %d/%d",
				c.typ, c.loc, e.Old.MonthlyNetCents, e.New.MonthlyNetCents, c.oldMonth, c.newMonth)
		}
		if e.Old.HourlyNetMilliCents != c.oldHourMilli || e.New.HourlyNetMilliCents != c.newHourMilli {
			t.Errorf("%s@%s hourly: got %d/%d want %d/%d",
				c.typ, c.loc, e.Old.HourlyNetMilliCents, e.New.HourlyNetMilliCents, c.oldHourMilli, c.newHourMilli)
		}
	}
}

// The monthly figure is a cap on accumulated hourly usage, not 730x the
// hourly rate — which is why the label convention carries both independently
// rather than deriving one. If a future table ever satisfies the derivation,
// that assumption deserves revisiting rather than silently holding.
func TestMonthlyIsNotDerivedFromHourly(t *testing.T) {
	table, _ := LoadTable("")
	for _, e := range table.ServerTypes {
		// monthly cents vs hourly milli-cents x 730: compare at milli-cent
		// scale, integers only.
		if e.Old.MonthlyNetCents*1000 == e.Old.HourlyNetMilliCents*730 {
			t.Errorf("%s@%v: monthly is exactly 730x hourly; the independence assumption may not hold",
				e.Name, e.Locations)
		}
	}
}

// Every table value must survive the round trip through its label, since the
// label is where it actually lives.
func TestLabelsCarryTheCurrency(t *testing.T) {
	table, _ := LoadTable("")
	l := table.ServerTypes[0].Labels("EUR")
	if l[LabelCurrency] != "EUR" {
		t.Fatalf("currency label: %q — an amount without a currency is not an amount", l[LabelCurrency])
	}
	if len(l) != 3 {
		t.Fatalf("labels: %+v", l)
	}
}

func TestLabelValuesRoundTrip(t *testing.T) {
	table, _ := LoadTable("")
	for _, e := range table.ServerTypes {
		labels := e.Labels("EUR")
		m, err := strconv.ParseInt(labels[LabelMonthly], 10, 64)
		if err != nil || m != e.Old.MonthlyNetCents {
			t.Errorf("%s monthly label %q does not round-trip", e.Name, labels[LabelMonthly])
		}
		h, err := strconv.ParseInt(labels[LabelHourly], 10, 64)
		if err != nil || h != e.Old.HourlyNetMilliCents {
			t.Errorf("%s hourly label %q does not round-trip", e.Name, labels[LabelHourly])
		}
	}
}

func TestValidateRejectsDuplicateTypeLocation(t *testing.T) {
	tbl := &Table{
		Currency: "EUR", Basis: "net", Effective: "2026-06-15T06:00:00Z",
		ServerTypes: []Entry{
			{Name: "cpx52", Locations: []string{"fsn1"}},
			{Name: "cpx52", Locations: []string{"fsn1", "nbg1"}},
		},
	}
	if err := tbl.Validate(); err == nil {
		t.Fatal("a duplicated (type, location) must be rejected — the price would depend on iteration order")
	}
}

func TestValidateRejectsBadTables(t *testing.T) {
	base := func() *Table {
		return &Table{
			Currency: "EUR", Basis: "net", Effective: "2026-06-15T06:00:00Z",
			ServerTypes: []Entry{{Name: "cpx52", Locations: []string{"fsn1"}}},
		}
	}
	// Hetzner's published page quotes gross. A table regenerated from it
	// without converting would overstate every server by the VAT rate while
	// looking entirely plausible, so the basis is declared and checked.
	t.Run("basis must be net", func(t *testing.T) {
		tbl := base()
		tbl.Basis = "gross"
		if err := tbl.Validate(); err == nil {
			t.Fatal("a gross table must be rejected")
		}
		tbl.Basis = ""
		if err := tbl.Validate(); err == nil {
			t.Fatal("a table with no declared basis must be rejected")
		}
	})
	t.Run("currency", func(t *testing.T) {
		tbl := base()
		tbl.Currency = "USD"
		if err := tbl.Validate(); err == nil {
			t.Fatal("non-EUR must be rejected")
		}
	})
	t.Run("effective", func(t *testing.T) {
		tbl := base()
		tbl.Effective = "yesterday"
		if err := tbl.Validate(); err == nil {
			t.Fatal("unparseable effective time must be rejected")
		}
	})
	t.Run("empty", func(t *testing.T) {
		tbl := base()
		tbl.ServerTypes = nil
		if err := tbl.Validate(); err == nil {
			t.Fatal("empty table must be rejected")
		}
	})
	t.Run("negative", func(t *testing.T) {
		tbl := base()
		tbl.ServerTypes[0].Old.MonthlyNetCents = -1
		if err := tbl.Validate(); err == nil {
			t.Fatal("negative price must be rejected")
		}
	})
	t.Run("no locations", func(t *testing.T) {
		tbl := base()
		tbl.ServerTypes[0].Locations = nil
		if err := tbl.Validate(); err == nil {
			t.Fatal("entry without locations must be rejected")
		}
	})
}

func TestMoneyFormatting(t *testing.T) {
	for _, c := range []struct {
		cents int64
		want  string
	}{
		{4342, "43.42"}, {534, "5.34"}, {101565, "1015.65"}, {0, "0.00"}, {7, "0.07"},
	} {
		if got := EURMonth(c.cents); got != c.want {
			t.Errorf("EURMonth(%d) = %q, want %q", c.cents, got, c.want)
		}
	}
	for _, c := range []struct {
		milli int64
		want  string
	}{
		{6960, "0.0696"}, {860, "0.0086"}, {2000, "0.0200"},
		{119570, "1.1957"}, {101750, "1.0175"}, {0, "0.0000"},
	} {
		if got := EURHour(c.milli); got != c.want {
			t.Errorf("EURHour(%d) = %q, want %q", c.milli, got, c.want)
		}
	}
}
