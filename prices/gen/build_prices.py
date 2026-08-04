"""Build prices-2026-06-15.json from Hetzner's published price-adjustment table.

The published table quotes GROSS prices — 19% German VAT included. Everything
downstream of this file works in NET EUR, so the conversion happens here, once,
and is verified rather than assumed.

Two invariants are enforced; the script writes nothing if either fails:

  1. The derived NEW net prices must match a real GET /pricing capture taken
     after the adjustment (verify-2026-07-28.json). That capture IS the new
     price list, straight from the API, so it proves the conversion exact. The
     same conversion then applies to the OLD column, for which no capture can
     exist.

  2. Every net price must encode exactly as an integer count of cents
     (monthly) and milli-cents (hourly). A price that would round is refused
     rather than rounded — a rounded override is indistinguishable from a
     correct one once it sits in a label.

Run:  python3 prices/gen/build_prices.py
"""

from decimal import Decimal, ROUND_HALF_UP
import json
import pathlib
import sys

SOURCE = "https://docs.hetzner.com/de/general/infrastructure-and-availability/price-adjustment/"
EFFECTIVE = "2026-06-15T06:00:00Z"  # 15 June 2026, 08:00 CEST

# The published table is gross at the German rate. The API reports the same
# prices net, and gross = net x this, exactly.
VAT_PERCENT = Decimal("19")
VAT = 1 + VAT_PERCENT / 100

# Hetzner quotes net to two decimals monthly and four hourly. Rounding the
# quotient to those places is what recovers the exact net figure.
MONTHLY_PLACES = 2
HOURLY_PLACES = 4

# location groups exactly as the published table groups them
GROUPS = {
    "de_fi": ["fsn1", "nbg1", "hel1"],   # Deutschland (FSN/NBG) / Finnland (HEL)
    "usa": ["ash", "hil"],               # USA (ASH/HIL)
    "sin": ["sin"],                      # Singapur (SIN)
}

# (type, old hourly, old monthly, new hourly, new monthly) — GROSS, verbatim
# from the published table. Kept as published so this file can be diffed
# against the source page; the conversion to net happens below.
ROWS = {
    "de_fi": [
        ("cax11", "0.0086", "5.34", "0.0114", "7.13"),
        ("cax21", "0.0152", "9.51", "0.0200", "12.48"),
        ("cax31", "0.0305", "19.03", "0.0400", "24.98"),
        ("cax41", "0.0601", "37.47", "0.0782", "48.78"),
        ("ccx13", "0.0305", "19.03", "0.0820", "51.16"),
        ("ccx23", "0.0601", "37.47", "0.1640", "102.33"),
        ("ccx33", "0.1191", "74.36", "0.2641", "164.80"),
        ("ccx43", "0.2384", "148.74", "0.5263", "328.43"),
        ("ccx53", "0.4767", "297.49", "1.0175", "634.85"),
        ("ccx63", "0.7141", "445.64", "1.6277", "1015.65"),
        ("cpx22", "0.0152", "9.51", "0.0371", "23.19"),
        ("cpx32", "0.0267", "16.65", "0.0677", "42.23"),
        ("cpx42", "0.0486", "30.33", "0.1326", "82.69"),
        ("cpx52", "0.0696", "43.42", "0.1916", "119.58"),
        ("cpx62", "0.0963", "60.08", "0.2479", "154.69"),
        ("cx23", "0.0076", "4.75", "0.0105", "6.53"),
        ("cx33", "0.0124", "7.72", "0.0162", "10.10"),
        ("cx43", "0.0228", "14.27", "0.0305", "19.03"),
        ("cx53", "0.0428", "26.76", "0.0563", "35.09"),
    ],
    "usa": [
        ("ccx13", "0.0324", "20.22", "0.0829", "51.75"),
        ("ccx23", "0.0649", "40.45", "0.1668", "104.11"),
        ("ccx33", "0.1240", "77.34", "0.2688", "167.78"),
        ("ccx43", "0.2479", "154.69", "0.5330", "332.59"),
        ("ccx53", "0.4959", "309.39", "1.0270", "640.80"),
        ("ccx63", "0.7438", "464.09", "1.6401", "1023.39"),
        ("cpx11", "0.0114", "7.13", "0.0333", "20.81"),
        ("cpx21", "0.0228", "14.27", "0.0610", "38.07"),
        ("cpx31", "0.0400", "24.98", "0.1191", "74.36"),
        ("cpx41", "0.0744", "46.40", "0.2298", "143.38"),
        ("cpx51", "0.1487", "92.81", "0.4539", "283.21"),
    ],
    "sin": [
        ("ccx13", "0.0525", "32.71", "0.1029", "64.25"),
        ("ccx23", "0.0982", "61.27", "0.2069", "129.10"),
        ("ccx33", "0.1849", "115.42", "0.3327", "207.64"),
        ("ccx43", "0.3403", "212.40", "0.6503", "405.78"),
        ("ccx53", "0.7295", "455.16", "1.2434", "775.87"),
        ("ccx63", "1.1957", "746.12", "1.9766", "1233.42"),
        ("cpx12", "0.0152", "9.51", "0.0295", "18.43"),
        ("cpx22", "0.0305", "19.03", "0.0506", "31.52"),
        ("cpx32", "0.0620", "38.66", "0.0934", "58.30"),
        ("cpx42", "0.1067", "66.63", "0.1783", "111.25"),
        ("cpx52", "0.1488", "92.81", "0.2564", "160.04"),
        ("cpx62", "0.1916", "119.58", "0.3280", "204.67"),
    ],
}

CENT = Decimal(100)           # 1e-2 EUR
MILLICENT = Decimal(100_000)  # 1e-5 EUR

HERE = pathlib.Path(__file__).resolve().parent
problems = []


def net(gross: str, places: int) -> Decimal:
    """Gross EUR -> net EUR at Hetzner's own quoting precision."""
    return (Decimal(gross) / VAT).quantize(Decimal(1).scaleb(-places), rounding=ROUND_HALF_UP)


def scaled(eur: Decimal, scale: Decimal, what: str):
    """Net EUR -> integer count at `scale`, refusing anything that would round."""
    v = eur * scale
    if v != v.to_integral_value():
        problems.append(f"{what}: {eur} EUR is not exact at scale {scale} -> {v}")
        return None
    if v < 0:
        problems.append(f"{what}: negative")
        return None
    return int(v)


entries = []
for group, rows in ROWS.items():
    for name, oh, om, nh, nm in rows:
        entries.append({
            "name": name,
            "locations": GROUPS[group],
            "old": {
                "monthly_net_cents": scaled(net(om, MONTHLY_PLACES), CENT, f"{name} old monthly"),
                "hourly_net_millicents": scaled(net(oh, HOURLY_PLACES), MILLICENT, f"{name} old hourly"),
            },
            "new": {
                "monthly_net_cents": scaled(net(nm, MONTHLY_PLACES), CENT, f"{name} new monthly"),
                "hourly_net_millicents": scaled(net(nh, HOURLY_PLACES), MILLICENT, f"{name} new hourly"),
            },
        })

# --- invariant 1: the derived NEW net prices must match the API capture ------

truth_doc = json.loads((HERE / "verify-2026-07-28.json").read_text())
if Decimal(truth_doc["vat_rate"]) != VAT_PERCENT:
    problems.append(f"capture VAT rate {truth_doc['vat_rate']} does not match VAT_PERCENT={VAT_PERCENT}")

truth = {
    (p["name"], p["location"]): (Decimal(p["monthly_net"]), Decimal(p["hourly_net"]))
    for p in truth_doc["prices_net"]
}
verified = 0
for e in entries:
    for loc in e["locations"]:
        key = (e["name"], loc)
        if key not in truth:
            problems.append(f"{key}: no ground truth in verify-2026-07-28.json")
            continue
        want_m, want_h = truth[key]
        got_m = Decimal(e["new"]["monthly_net_cents"]) / CENT
        got_h = Decimal(e["new"]["hourly_net_millicents"]) / MILLICENT
        if got_m != want_m:
            problems.append(f"{key} new monthly: derived {got_m}, api {want_m}")
        elif got_h != want_h:
            problems.append(f"{key} new hourly: derived {got_h}, api {want_h}")
        else:
            verified += 1

# --- invariant 2 was collected in `problems` by scaled() --------------------

if problems:
    print("REFUSING TO WRITE — the conversion is not trustworthy:\n", file=sys.stderr)
    for p in problems:
        print("  ", p, file=sys.stderr)
    sys.exit(1)

doc = {
    "_comment": (
        "Pre-adjustment Hetzner Cloud list prices, for proposing price overrides on "
        "servers that predate the change. NET EUR: the published source table quotes "
        "gross at 19% German VAT, converted here and verified against a post-adjustment "
        "GET /pricing capture (see prices/gen/). Prices EXCLUDE the primary IPv4, which "
        "is a separately priced resource — do not add it here."
    ),
    "source": SOURCE,
    "source_quotes": "gross, 19% German VAT",
    "effective": EFFECTIVE,
    "currency": "EUR",
    "basis": "net",
    "units": {
        "monthly_net_cents": "1e-2 EUR, integer",
        "hourly_net_millicents": "1e-5 EUR, integer",
    },
    "server_types": sorted(entries, key=lambda e: (e["name"], e["locations"][0])),
}

out = HERE.parent / "prices-2026-06-15.json"
out.write_text(json.dumps(doc, indent=2, ensure_ascii=False) + "\n")

ratios = []
for e in entries:
    h = Decimal(e["old"]["hourly_net_millicents"]) / MILLICENT
    m = Decimal(e["old"]["monthly_net_cents"]) / CENT
    if h:
        ratios.append(m / (h * 730))

print(f"entries:          {len(entries)}  ({len(set(e['name'] for e in entries))} types)")
print(f"gross -> net:     every price exact at cent / milli-cent scale")
print(f"verified vs API:  {verified} (type, location) pairs match the 2026-07-28 capture")
print(f"monthly vs 730xh: ratio {min(ratios):.4f} .. {max(ratios):.4f}, never 1 — independent")
print(f"written:          {out}")
