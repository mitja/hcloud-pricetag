package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// DefaultNamespace prefixes the three reserved label keys unless -namespace or
// PRICETAG_NAMESPACE says otherwise. Vaicoa reads these keys.
const DefaultNamespace = "vaicoa.com"

// NamespaceEnv is the environment variable that sets the namespace when the
// -namespace flag is not given.
const NamespaceEnv = "PRICETAG_NAMESPACE"

// Label key suffixes. The values are integer strings, and the units are the
// whole point:
//
//	monthly  cents       1e-2 of the currency  "3649"  -> 36.49/month
//	hourly   milli-cents 1e-5 of the currency  "5850"  -> 0.0585/hour
//
// NET, always. Hetzner's API reports both net and gross and bills on net; the
// reader stores net and applies VAT at display time. Hetzner's published
// price-adjustment page quotes GROSS (19% German VAT) — the bundled table is
// converted, and Table.Validate refuses a table that is not net.
//
// Cents are too coarse for an hourly rate. Hetzner quotes hourly prices to
// four decimals, so a rate of 0.0072 would land on 0.01 at cent granularity —
// a 39% error, the same order as the bug this fixes.
const (
	suffixMonthly = "monthly-net-cents"
	suffixHourly  = "hourly-net-millicents"
	// The currency label is required: an amount without a stated currency is
	// not an amount. The reader compares it against the project's own
	// /pricing document and ignores the override when they disagree.
	suffixCurrency = "price-currency"
)

// Scales of the two amount labels, in decimal places of the currency unit.
const (
	MonthlyScale = 2 // cents
	HourlyScale  = 5 // milli-cents
)

// LabelKeys are the three reserved keys under one namespace.
type LabelKeys struct {
	Namespace string
	Monthly   string
	Hourly    string
	Currency  string
}

// Hetzner label keys follow the Kubernetes convention: an optional DNS
// subdomain prefix (at most 253 characters), a slash, and a name.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// KeysFor builds the reserved keys for a namespace, refusing one Hetzner would
// reject as a label prefix. The check runs before any API call, so a typo
// fails here rather than as an HTTP 422 halfway through a run.
func KeysFor(ns string) (LabelKeys, error) {
	if ns == "" {
		return LabelKeys{}, fmt.Errorf("label namespace is empty")
	}
	if len(ns) > 253 {
		return LabelKeys{}, fmt.Errorf("label namespace %q is longer than 253 characters", ns)
	}
	for _, part := range strings.Split(ns, ".") {
		if !dnsLabel.MatchString(part) {
			return LabelKeys{}, fmt.Errorf("label namespace %q is not a lowercase DNS subdomain (like %s)", ns, DefaultNamespace)
		}
	}
	return LabelKeys{
		Namespace: ns,
		Monthly:   ns + "/" + suffixMonthly,
		Hourly:    ns + "/" + suffixHourly,
		Currency:  ns + "/" + suffixCurrency,
	}, nil
}

// ResolveNamespace picks the namespace: the flag when set, else the
// environment variable, else DefaultNamespace.
func ResolveNamespace(flagValue string, getenv func(string) string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	if v := strings.TrimSpace(getenv(NamespaceEnv)); v != "" {
		return v
	}
	return DefaultNamespace
}

// keysFromFlag is ResolveNamespace + KeysFor against the real environment.
func keysFromFlag(flagValue string) (LabelKeys, error) {
	return KeysFor(ResolveNamespace(flagValue, os.Getenv))
}

// All returns the three keys.
func (k LabelKeys) All() []string { return []string{k.Monthly, k.Hourly, k.Currency} }

// ParseMinor converts a decimal amount such as "5.34" or "0.0073" into an
// integer count of 1/10^scale units: ParseMinor("5.34", 2) == 534 cents,
// ParseMinor("0.0073", 5) == 730 milli-cents.
//
// The rounding rule is: there is none. An amount with more significant
// fractional digits than the scale holds is REFUSED, never rounded —
// ParseMinor("5.345", 2) and ParseMinor("0.00725", 2) are errors, while
// ParseMinor("0.00725", 5) == 725 and ParseMinor("5.3450", 2) is still an
// error but ParseMinor("5.3400", 2) == 534. This follows the label
// convention's rule (specs/16 §2.1) that a writer must refuse a value it
// cannot represent exactly: once a rounded number is in the label it is
// indistinguishable from a correct one.
//
// Only digits and one '.' are accepted. A comma is refused rather than
// guessed at, since "1,234" means different things in different locales.
// Parsing is done on the digit string; no float is involved.
func ParseMinor(s string, scale int) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}
	if strings.Contains(s, ",") {
		return 0, fmt.Errorf("%q: use a dot as the decimal separator, without thousands separators", s)
	}
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("%q: a price cannot be negative", s)
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" && frac == "" {
		return 0, fmt.Errorf("%q is not a decimal amount like 5.34", s)
	}
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || !allDigits(frac) || strings.Contains(frac, ".") {
		return 0, fmt.Errorf("%q is not a decimal amount like 5.34", s)
	}
	trimmed := strings.TrimRight(frac, "0")
	if len(trimmed) > scale {
		return 0, fmt.Errorf("%q has more than %d decimal places; it would have to be rounded, and an override is never rounded", s, scale)
	}
	digits := strings.TrimLeft(whole+trimmed+strings.Repeat("0", scale-len(trimmed)), "0")
	if digits == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	return n, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
