# hcloud-pricetag

Label Hetzner Cloud servers with the price they **actually** pay.

Hetzner's `GET /pricing` only ever reports *current* list prices. After the
[price adjustment of 15 June 2026][pa], a server created before that date keeps
its old rate — but nothing in the API says so. There is no historical price
endpoint and no per-resource price, so any tool that reads your account sees
the new price and reports it for every server, old or new.

The gap is not small. In Falkenstein a CPX52 went from €36.49 to €100.49 net per
month: a cost report that trusts `/pricing` overstates that one server by 175%.

`hcloud-pricetag` closes the gap by writing the real price onto the server as a
label, taken from a bundled table of the published pre-adjustment prices.

[pa]: https://docs.hetzner.com/de/general/infrastructure-and-availability/price-adjustment/

## The thing this tool will not do for you

**Being created before the cutoff does not prove a server kept its old price.**
Rescaling forfeits it, and the Hetzner API does not record that a server was
ever rescaled. Creation date is necessary evidence, not sufficient evidence.

So this tool proposes and you confirm. There is deliberately no flag that
confirms everything at once — it would let you generate confidently wrong
numbers faster than you could check them, which is the exact problem the tool
exists to fix.

You are the only one who knows which machines were left alone.

## Install

```sh
go install github.com/mitja/hcloud-pricetag@latest
```

No dependencies beyond the Go standard library. This tool writes to your
infrastructure with a write-scoped token; a dependency tree would be something
you have to audit too.

## Use

```sh
export HCLOUD_TOKEN=...          # needs read+write; labels are a write

hcloud-pricetag plan             # reads only, writes pricetag-plan.json
$EDITOR pricetag-plan.json       # set "confirmed": true where you know it holds
hcloud-pricetag apply            # writes the labels
```

`plan` prints what it proposes and, just as importantly, what it skipped and
why:

```
inspected 12 server(s), cutoff 2026-06-15T06:00:00Z
proposed  3
  141232610  web-1        cpx32    nbg1      35.49 ->    13.99 EUR/month
  141232611  db-1         ccx33    fsn1     138.49 ->    62.49 EUR/month
  141232612  edge-1       cax11    hel1       5.99 ->     4.49 EUR/month
skipped   9
  141232613  build-1      created at or after the cutoff — already on current pricing
  141232614  odd-1        no pre-adjustment price for cx22 in fsn1 in this table
```

A skipped server is never silently dropped. A type missing from the table gets
no proposal at all rather than a guess.

To undo everything:

```sh
hcloud-pricetag revert -dry-run
hcloud-pricetag revert -yes
```

### Currency

Hetzner bills in EUR or USD, fixed when the account is created. `plan` reads
the account's currency from `/pricing` and **refuses to run** on anything but
EUR, because the bundled table holds EUR figures and no USD equivalent ships
with this tool. Writing euro amounts onto a dollar-billed account would be
wrong by an exchange rate while looking entirely reasonable.

Supply your own table with `-prices` if you have the USD figures.

### Authentication

`HCLOUD_TOKEN`, or `-token-file PATH`. There is no `-token` flag on purpose: a
write-scoped token does not belong in your shell history or in `ps` output.

Set `HCLOUD_API_BASE` to try the tool against a mock instead of the real API.

## What it writes

Three labels, on the server:

| Label | Unit | Example |
|---|---|---|
| `cloudviewer.app/monthly-net-cents` | cents, 1e-2 of the unit | `1399` → 13.99/month |
| `cloudviewer.app/hourly-net-millicents` | milli-cents, 1e-5 of the unit | `2240` → 0.0224/hour |
| `cloudviewer.app/price-currency` | ISO code | `EUR` |

The currency label is required, and the reader compares it against the
project's own `/pricing` document rather than trusting it. An amount without a
stated currency is not an amount — and Hetzner allows moving a server between
projects, where crossing a currency boundary re-prices it, leaving a stale
label wrong twice over.

Both are **net** EUR, excluding the primary IPv4 (which Hetzner prices
separately).

**Net, not gross.** Hetzner's API reports both and bills on net, and the
consumer of these labels stores net and applies VAT at display time. Hetzner's
published adjustment page, however, quotes **gross** at the 19% German rate.
The bundled table is converted, the table declares `"basis": "net"`, and the
tool refuses to load one that does not — because a table built straight from
the page would overstate every labelled server by 19% while looking entirely
plausible.

**Why two different units.** Hetzner quotes hourly prices to four decimals in
EUR. At cent granularity a rate of €0.0072/h would round to €0.01 — a 39%
error, reintroducing in the fix an error of the same order as the bug. Every
price in the bundled table encodes exactly at these two scales, and the
generator refuses to emit one that would not.

**Why both, rather than deriving one.** Hetzner's monthly figure is a cap on
accumulated hourly usage, not 730 × the hourly rate. Across the 42 entries in
the table the ratio ranges from 0.854 to 0.856 and is never 1 — a derived
monthly price would be about 15% too high.

Your existing labels are preserved. `PUT /servers/{id}` replaces the whole
label map rather than merging, so every write here starts from the server's
current labels and adds to them.

## Safety properties

- `plan` never writes to Hetzner.
- A fresh plan arrives with every entry `"confirmed": false`.
- `apply` re-reads the live servers and **refuses** any whose type, location or
  creation timestamp changed since the plan was made. A rescale between plan
  and apply is precisely the event that forfeits the old price.
- Re-running either command changes nothing that is already correct.
- `revert` removes only the three reserved keys.

## The price table

`prices/prices-2026-06-15.json` — 42 entries, 25 server types, 6 locations,
with the source URL and the effective timestamp in the file.

Prices are keyed by **type and location**: 11 of the 25 types cost different
amounts per region. A cpx52 was €36.49/month net in Falkenstein and €77.99 in
Singapore, so a lookup by type alone would be wrong by a factor of two.

Each entry carries the `new` price as well as the `old` one. The tool does not
need it to write labels — it is there so a future version can notice that
`/pricing` no longer matches, which means another adjustment has happened and
the table has gone stale.

A future adjustment gets a **new** file. Existing ones are never edited, so a
rerun years from now still reproduces its original proposal.

To regenerate from the published figures:

```sh
python3 prices/gen/build_prices.py
```

The generator converts gross → net with exact decimal arithmetic and enforces
two invariants, writing nothing if either fails:

1. The derived **new** net prices must match `prices/gen/verify-2026-07-28.json`,
   a real `GET /pricing` capture taken after the adjustment — 91 (type,
   location) pairs of API ground truth. That is what proves the VAT conversion
   exact, and the same conversion then carries the **old** column, for which no
   capture can exist.
2. Every net price must encode exactly at cent and milli-cent scale. One that
   would round is refused, not rounded.

No float touches a price anywhere in this repository. CI runs the generator and
fails if the checked-in table differs.

## Relationship to Cloud Viewer

The label keys are namespaced to [Cloud Viewer][cv], which reads them and
prefers them over the list price. The convention is not a standard, and the
prefix says so honestly.

The labels are inert to everything else — nothing else reads them, and Hetzner
bills you what it bills you regardless.

[cv]: https://cloudviewer.app

## License

MIT.
