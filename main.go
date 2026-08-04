// Command hcloud-pricetag labels Hetzner Cloud servers with the price they
// actually pay, for servers that predate a price adjustment and kept their old
// rate.
//
// Hetzner's GET /pricing only ever reports current list prices. There is no
// historical price endpoint and no per-resource price, so a tool that reads
// the API cannot tell what a grandfathered server costs. This one writes the
// number onto the server as a label, from a bundled table of the published
// pre-adjustment prices — with the operator confirming which servers still
// qualify, because that is the one fact the API does not carry.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const defaultPlanFile = "pricetag-plan.json"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hcloud-pricetag: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `hcloud-pricetag — label Hetzner Cloud servers with their grandfathered price

  hcloud-pricetag plan    [-o FILE] [-prices FILE] [-cutoff TIME]
      Read your servers, propose an override for each one that predates the
      price adjustment, and write a plan file. Reads only; writes nothing to
      Hetzner.

  hcloud-pricetag apply   [-plan FILE] [-dry-run]
      Write the labels for entries you marked "confirmed": true. Refuses any
      server that changed since the plan was made.

  hcloud-pricetag revert  [-yes] [-dry-run]
      Remove both override labels from every server that carries them.

  hcloud-pricetag prices  [-prices FILE]
      Print the bundled price table.

Authentication
  HCLOUD_TOKEN=…                a read+write Hetzner Cloud API token
  -token-file PATH              read the token from a file instead

  There is deliberately no -token flag: a write-scoped token does not belong
  in your shell history or in ps output.

  HCLOUD_API_BASE=…             point at a mock instead of api.hetzner.cloud
`)
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("no command given")
	}
	switch args[0] {
	case "plan":
		return cmdPlan(args[1:])
	case "apply":
		return cmdApply(args[1:])
	case "revert":
		return cmdRevert(args[1:])
	case "prices":
		return cmdPrices(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// token resolves the API token. Env first, matching the hcloud CLI.
func token(tokenFile string) (string, error) {
	if tokenFile != "" {
		raw, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("read token file: %w", err)
		}
		t := strings.TrimSpace(string(raw))
		if t == "" {
			return "", fmt.Errorf("token file %s is empty", tokenFile)
		}
		return t, nil
	}
	if t := strings.TrimSpace(os.Getenv("HCLOUD_TOKEN")); t != "" {
		return t, nil
	}
	return "", fmt.Errorf("no token: set HCLOUD_TOKEN or pass -token-file")
}

func cmdPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	out := fs.String("o", defaultPlanFile, "write the plan here")
	pricesPath := fs.String("prices", "", "price table to use instead of the bundled one")
	cutoffStr := fs.String("cutoff", "", "RFC3339 cutoff; defaults to the table's effective time")
	tokenFile := fs.String("token-file", "", "read the API token from this file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	table, err := LoadTable(*pricesPath)
	if err != nil {
		return err
	}
	cutoff, err := table.EffectiveTime()
	if err != nil {
		return err
	}
	if *cutoffStr != "" {
		if cutoff, err = time.Parse(time.RFC3339, *cutoffStr); err != nil {
			return fmt.Errorf("bad -cutoff: %w", err)
		}
	}
	tok, err := token(*tokenFile)
	if err != nil {
		return err
	}

	ctx := context.Background()
	client := NewClient(os.Getenv("HCLOUD_API_BASE"), tok)
	servers, err := client.ListServers(ctx)
	if err != nil {
		return err
	}

	plan := BuildPlan(servers, table, cutoff)
	if err := plan.Save(*out); err != nil {
		return err
	}

	fmt.Printf("inspected %d server(s), cutoff %s\n", plan.TotalInspected, cutoff.Format(time.RFC3339))
	fmt.Printf("proposed  %d\n", len(plan.Servers))
	for _, e := range plan.Servers {
		fmt.Printf("  %-10d %-24s %-8s %-6s %8s -> %8s EUR/month\n",
			e.ID, truncate(e.Name, 24), e.ServerType, e.Location,
			e.CurrentListEURMonth, e.ProposedEURMonth)
	}
	// Skipped servers are printed, not just counted: a quiet skip reads as
	// "covered everything".
	fmt.Printf("skipped   %d\n", len(plan.Skipped))
	for _, s := range plan.Skipped {
		fmt.Printf("  %-10d %-24s %s\n", s.ID, truncate(s.Name, 24), s.Reason)
	}
	fmt.Printf("\nwrote %s — set \"confirmed\": true on the servers you know were never\nrescaled, then run: hcloud-pricetag apply\n", *out)
	return nil
}

func cmdApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	planPath := fs.String("plan", defaultPlanFile, "plan file to apply")
	dryRun := fs.Bool("dry-run", false, "show what would be written, write nothing")
	tokenFile := fs.String("token-file", "", "read the API token from this file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	plan, err := LoadPlan(*planPath)
	if err != nil {
		return err
	}
	tok, err := token(*tokenFile)
	if err != nil {
		return err
	}

	ctx := context.Background()
	client := NewClient(os.Getenv("HCLOUD_API_BASE"), tok)
	// Re-read rather than trusting the plan: the drift check is the point.
	live, err := client.ListServers(ctx)
	if err != nil {
		return err
	}

	results, applyErr := Apply(ctx, client, plan, live, *dryRun)
	written, skipped := 0, 0
	for _, r := range results {
		if r.Written {
			written++
			verb := "labelled"
			if *dryRun {
				verb = "would label"
			}
			fmt.Printf("  %-10d %-24s %s %s EUR/month\n", r.Entry.ID, truncate(r.Entry.Name, 24), verb, r.Entry.ProposedEURMonth)
			continue
		}
		skipped++
		// "not confirmed" is the normal state of an unedited plan, not news.
		if r.Skipped != "not confirmed" {
			fmt.Printf("  %-10d %-24s SKIPPED: %s\n", r.Entry.ID, truncate(r.Entry.Name, 24), r.Skipped)
		}
	}
	if applyErr != nil {
		return applyErr
	}
	fmt.Printf("\n%d written, %d skipped\n", written, skipped)
	if written == 0 && skipped > 0 {
		fmt.Printf("nothing was confirmed in %s\n", *planPath)
	}
	return nil
}

func cmdRevert(args []string) error {
	fs := flag.NewFlagSet("revert", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "required: strip the override labels from every server that has them")
	dryRun := fs.Bool("dry-run", false, "show what would change, write nothing")
	tokenFile := fs.String("token-file", "", "read the API token from this file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*yes && !*dryRun {
		return fmt.Errorf("revert touches every labelled server at once — pass -dry-run to preview or -yes to proceed")
	}
	tok, err := token(*tokenFile)
	if err != nil {
		return err
	}

	ctx := context.Background()
	client := NewClient(os.Getenv("HCLOUD_API_BASE"), tok)
	servers, err := client.ListServers(ctx)
	if err != nil {
		return err
	}

	n := 0
	for _, s := range servers {
		stripped, changed := StripOverrides(s.Labels)
		if !changed {
			continue
		}
		n++
		verb := "removed override from"
		if *dryRun {
			verb = "would remove override from"
		} else if err := client.SetLabels(ctx, s.ID, stripped); err != nil {
			return fmt.Errorf("server %d (%s): %w", s.ID, s.Name, err)
		}
		fmt.Printf("  %-10d %-24s %s\n", s.ID, truncate(s.Name, 24), verb)
	}
	fmt.Printf("\n%d server(s) carried an override\n", n)
	return nil
}

func cmdPrices(args []string) error {
	fs := flag.NewFlagSet("prices", flag.ContinueOnError)
	pricesPath := fs.String("prices", "", "price table to print instead of the bundled one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	table, err := LoadTable(*pricesPath)
	if err != nil {
		return err
	}
	fmt.Printf("source:    %s\n", table.Source)
	fmt.Printf("effective: %s\n\n", table.Effective)
	fmt.Printf("%-8s %-20s %10s %10s %12s %12s\n", "type", "locations", "old/mo", "new/mo", "old/h", "new/h")
	for _, e := range table.ServerTypes {
		fmt.Printf("%-8s %-20s %10s %10s %12s %12s\n",
			e.Name, strings.Join(e.Locations, ","),
			EURMonth(e.Old.MonthlyNetCents), EURMonth(e.New.MonthlyNetCents),
			EURHour(e.Old.HourlyNetMilliCents), EURHour(e.New.HourlyNetMilliCents))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
