package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Reserved label keys. This is the convention Cloud Viewer reads; the values
// are integer strings, and the units are the whole point:
//
//	monthly  cents       1e-2 EUR   "3649"  -> 36.49 EUR/month
//	hourly   milli-cents 1e-5 EUR   "5850"  -> 0.0585 EUR/hour
//
// NET, always. Hetzner's API reports both net and gross and bills on net;
// Cloud Viewer stores net and applies VAT at display time. Note that Hetzner's
// published price-adjustment page quotes GROSS (19% German VAT) — the bundled
// table is converted, and Table.Validate refuses a table that is not net.
//
// Cents are too coarse for an hourly rate. Hetzner quotes hourly prices to
// four decimals in EUR, so a rate of 0.0072 would land on 0.01 at cent
// granularity — a 39% error, the same order as the bug this fixes. Every price
// in the bundled table encodes exactly at both scales; the generator refuses
// to emit one that would not (prices/gen/build_prices.py).
const (
	LabelMonthly = "cloudviewer.app/monthly-net-cents"
	LabelHourly  = "cloudviewer.app/hourly-net-millicents"
	// LabelCurrency says which currency the two amounts are in, and is
	// required: an amount without a stated currency is not an amount. The
	// reader compares it against the project's own /pricing document and
	// ignores the override when they disagree.
	LabelCurrency = "cloudviewer.app/price-currency"
)

// TableCurrency is the only billing currency the bundled table covers.
// Hetzner's published adjustment page quotes EUR; no USD equivalent is
// bundled, so a USD-billed account gets refused rather than mislabelled.
const TableCurrency = "EUR"

//go:embed prices/prices-2026-06-15.json
var embeddedTable []byte

// Price is one (monthly, hourly) pair, both net EUR as integer counts.
type Price struct {
	MonthlyNetCents     int64 `json:"monthly_net_cents"`
	HourlyNetMilliCents int64 `json:"hourly_net_millicents"`
}

// Entry is one server type at one group of locations, before and after a
// price adjustment.
//
// Locations matter: 11 of the 25 types in the bundled table appear in more
// than one region at different prices — cpx52 was 43.42 EUR/month in
// Falkenstein and 92.81 in Singapore. A lookup by type alone would be
// silently wrong by a factor of two.
type Entry struct {
	Name      string   `json:"name"`
	Locations []string `json:"locations"`
	Old       Price    `json:"old"`
	New       Price    `json:"new"`
}

// Table is a price list captured at one adjustment.
type Table struct {
	Source string `json:"source"`
	// SourceQuotes records what the source page quotes, which is not
	// necessarily what this table holds: Hetzner's German page is gross.
	SourceQuotes string `json:"source_quotes,omitempty"`
	Effective    string `json:"effective"`
	Currency     string `json:"currency"`
	// Basis must be "net". It exists so a table regenerated from gross
	// figures fails loudly here instead of quietly overstating every server
	// by the VAT rate.
	Basis       string  `json:"basis"`
	ServerTypes []Entry `json:"server_types"`
}

// LoadTable reads the bundled table, or one from disk when path is set.
func LoadTable(path string) (*Table, error) {
	raw := embeddedTable
	if path != "" {
		var err error
		raw, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read price table: %w", err)
		}
	}
	var t Table
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("parse price table: %w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// Validate rejects a table that cannot be used safely. A hand-edited table is
// the one input nobody reviews, so the checks are strict: a duplicated
// (type, location) would make the price depend on iteration order.
func (t *Table) Validate() error {
	if t.Currency != "EUR" {
		return fmt.Errorf("price table: currency is %q, only EUR is supported", t.Currency)
	}
	// The published source quotes gross. A table built from it without
	// converting would overstate every labelled server by the VAT rate, and
	// the numbers would look entirely plausible while doing it.
	if t.Basis != "net" {
		return fmt.Errorf("price table: basis is %q, must be \"net\" (the labels are net EUR; Hetzner's published table is gross)", t.Basis)
	}
	if _, err := t.EffectiveTime(); err != nil {
		return err
	}
	if len(t.ServerTypes) == 0 {
		return fmt.Errorf("price table: no server types")
	}
	seen := map[string]bool{}
	for _, e := range t.ServerTypes {
		if e.Name == "" {
			return fmt.Errorf("price table: entry with no name")
		}
		if len(e.Locations) == 0 {
			return fmt.Errorf("price table: %s has no locations", e.Name)
		}
		if e.Old.MonthlyNetCents < 0 || e.Old.HourlyNetMilliCents < 0 ||
			e.New.MonthlyNetCents < 0 || e.New.HourlyNetMilliCents < 0 {
			return fmt.Errorf("price table: %s has a negative price", e.Name)
		}
		for _, loc := range e.Locations {
			k := e.Name + "@" + loc
			if seen[k] {
				return fmt.Errorf("price table: %s listed twice for %s", e.Name, loc)
			}
			seen[k] = true
		}
	}
	return nil
}

// EffectiveTime is the moment the new prices took effect. Servers created
// before it kept their old rate — unless they were rescaled, which this tool
// cannot see (see README).
func (t *Table) EffectiveTime() (time.Time, error) {
	ts, err := time.Parse(time.RFC3339, t.Effective)
	if err != nil {
		return time.Time{}, fmt.Errorf("price table: bad effective timestamp %q: %w", t.Effective, err)
	}
	return ts, nil
}

// Lookup finds the pre-adjustment price for a server type at a location.
func (t *Table) Lookup(serverType, location string) (Entry, bool) {
	name := strings.ToLower(serverType)
	for _, e := range t.ServerTypes {
		if e.Name != name {
			continue
		}
		for _, l := range e.Locations {
			if l == location {
				return e, true
			}
		}
	}
	return Entry{}, false
}

// Labels renders the override labels for this entry's OLD price.
func (e Entry) Labels(currency string) map[string]string {
	return map[string]string{
		LabelMonthly:  strconv.FormatInt(e.Old.MonthlyNetCents, 10),
		LabelHourly:   strconv.FormatInt(e.Old.HourlyNetMilliCents, 10),
		LabelCurrency: currency,
	}
}

// EURMonth renders a cent count as "43.42".
func EURMonth(cents int64) string { return formatScaled(cents, 2) }

// EURHour renders a milli-cent count as "0.0696" — trailing zeros beyond the
// four decimals Hetzner publishes are noise.
func EURHour(milliCents int64) string {
	return trimTrailingZeros(formatScaled(milliCents, 5), 4)
}

// formatScaled renders an integer count of 1/10^decimals EUR as an exact
// decimal string. The digits are placed, never computed: no float touches a
// price anywhere in this tool.
func formatScaled(n int64, decimals int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s) <= decimals {
		s = strings.Repeat("0", decimals-len(s)+1) + s
	}
	out := s[:len(s)-decimals] + "." + s[len(s)-decimals:]
	if neg {
		out = "-" + out
	}
	return out
}

func trimTrailingZeros(s string, minDecimals int) string {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return s
	}
	for len(s)-dot-1 > minDecimals && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	return s
}
