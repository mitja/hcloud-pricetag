package main

import (
	"strings"
	"testing"
)

func TestKeysForDefaultNamespace(t *testing.T) {
	k, err := KeysFor(DefaultNamespace)
	if err != nil {
		t.Fatal(err)
	}
	if k.Monthly != "vaicoa.com/monthly-net-cents" ||
		k.Hourly != "vaicoa.com/hourly-net-millicents" ||
		k.Currency != "vaicoa.com/price-currency" {
		t.Fatalf("keys: %+v", k)
	}
}

func TestKeysForCustomNamespace(t *testing.T) {
	k, err := KeysFor("prices.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if k.Monthly != "prices.example.org/monthly-net-cents" || k.Namespace != "prices.example.org" {
		t.Fatalf("keys: %+v", k)
	}
}

func TestKeysForRejectsInvalidNamespace(t *testing.T) {
	for _, ns := range []string{"", "Vaicoa.com", "vaicoa.com/", "-x.com", "x..com", "a b", "x_y.com", strings.Repeat("a.", 127) + "com"} {
		if _, err := KeysFor(ns); err == nil {
			t.Errorf("namespace %q accepted", ns)
		}
	}
}

func TestResolveNamespacePrecedence(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	if got := ResolveNamespace("", getenv); got != DefaultNamespace {
		t.Errorf("default: %q", got)
	}
	env[NamespaceEnv] = "env.example"
	if got := ResolveNamespace("", getenv); got != "env.example" {
		t.Errorf("env: %q", got)
	}
	if got := ResolveNamespace("flag.example", getenv); got != "flag.example" {
		t.Errorf("flag must win over env, got %q", got)
	}
}

func TestParseMinor(t *testing.T) {
	cases := []struct {
		in    string
		scale int
		want  int64
	}{
		{"5.34", MonthlyScale, 534},
		{"5", MonthlyScale, 500},
		{"5.", MonthlyScale, 500},
		{".5", MonthlyScale, 50},
		{"0", MonthlyScale, 0},
		{"0.00", MonthlyScale, 0},
		{"5.3400", MonthlyScale, 534}, // trailing zeros are not precision
		{" 36.49 ", MonthlyScale, 3649},
		{"100.49", MonthlyScale, 10049},
		{"0.0073", HourlyScale, 730},
		{"0.0072", HourlyScale, 720},
		{"0.00725", HourlyScale, 725}, // five decimals fit milli-cents exactly
		{"5.345", HourlyScale, 534500},
		{"0.0072000000", HourlyScale, 720}, // the shape /pricing returns
		{"4.4900000000", MonthlyScale, 449},
	}
	for _, c := range cases {
		got, err := ParseMinor(c.in, c.scale)
		if err != nil {
			t.Errorf("ParseMinor(%q, %d): %v", c.in, c.scale, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMinor(%q, %d) = %d, want %d", c.in, c.scale, got, c.want)
		}
	}
}

// The rounding rule is that there is none: an amount finer than the label's
// scale is refused, not rounded half-up, half-even or truncated. 5.345 would
// be 534 or 535 cents depending on the rule, and either is indistinguishable
// from a correct value once it sits in the label (specs/16 §2.1).
func TestParseMinorRefusesToRound(t *testing.T) {
	for _, c := range []struct {
		in    string
		scale int
	}{
		{"5.345", MonthlyScale},
		{"0.00725", MonthlyScale},
		{"5.3450", MonthlyScale},
		{"0.000001", HourlyScale},
		{"0.000075", HourlyScale},
	} {
		if n, err := ParseMinor(c.in, c.scale); err == nil {
			t.Errorf("ParseMinor(%q, %d) = %d, want an error", c.in, c.scale, n)
		} else if !strings.Contains(err.Error(), "rounded") {
			t.Errorf("ParseMinor(%q, %d): error should say why: %v", c.in, c.scale, err)
		}
	}
}

func TestParseMinorRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "-1", "-0.5", "5,34", "1,234.00", "abc", "1.2.3", "1e3", "+5", "0x10", ".", "99999999999999999999"} {
		if n, err := ParseMinor(in, MonthlyScale); err == nil {
			t.Errorf("ParseMinor(%q) = %d, want an error", in, n)
		}
	}
}
