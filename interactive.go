package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// interactiveAPI is the slice of Client the interactive mode needs, so the
// flow is testable against a fake.
type interactiveAPI interface {
	ListServers(ctx context.Context) ([]Server, error)
	GetServer(ctx context.Context, id int64) (Server, error)
	Pricing(ctx context.Context) (Pricing, error)
	SetLabels(ctx context.Context, id int64, labels map[string]string) error
}

// errQuit ends the session cleanly (q at a prompt, or end of input).
var errQuit = errors.New("quit")

// Interactive lets an operator set a price by hand: pick a server, see its
// current override and list price, type the net amounts, confirm, write.
//
// This is for prices the bundled table cannot know — an invoice, a contract,
// a server type the table does not cover. Unlike plan/apply it carries no
// drift check against a plan, because there is no plan: the operator states
// the price directly and confirms each write.
type Interactive struct {
	API  interactiveAPI
	Keys LabelKeys
	In   *bufio.Reader
	Out  io.Writer
}

// Run is the session loop. It returns nil when the operator quits.
func (it *Interactive) Run(ctx context.Context) error {
	pricing, err := it.API.Pricing(ctx)
	if err != nil {
		return fmt.Errorf("read /pricing: %w", err)
	}
	if pricing.Currency == "" {
		return fmt.Errorf("/pricing reported no currency; refusing to guess one")
	}
	servers, err := it.API.ListServers(ctx)
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		fmt.Fprintln(it.Out, "this project has no servers")
		return nil
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].ID < servers[j].ID })

	fmt.Fprintf(it.Out, "account currency %s, label namespace %s\n", pricing.Currency, it.Keys.Namespace)
	for {
		it.printList(servers)
		idx, err := it.pickServer(servers)
		if errors.Is(err, errQuit) {
			return nil
		}
		if err != nil {
			return err
		}
		updated, err := it.editServer(ctx, servers[idx], pricing)
		if errors.Is(err, errQuit) {
			return nil
		}
		if err != nil {
			return err
		}
		servers[idx] = updated
	}
}

func (it *Interactive) printList(servers []Server) {
	fmt.Fprintln(it.Out)
	fmt.Fprintf(it.Out, "  %3s  %-10s %-24s %-8s %-6s %s\n", "#", "id", "name", "type", "loc", "override")
	for i, s := range servers {
		fmt.Fprintf(it.Out, "  %3d  %-10d %-24s %-8s %-6s %s\n",
			i+1, s.ID, truncate(s.Name, 24), s.Type, s.Location, it.describe(s.Labels))
	}
}

// describe renders the current override labels, raw values included when
// they are malformed, since the operator needs to see what is actually there.
func (it *Interactive) describe(labels map[string]string) string {
	var parts []string
	if v, ok := labels[it.Keys.Monthly]; ok {
		parts = append(parts, fmtLabel(v, MonthlyScale, "/mo"))
	}
	if v, ok := labels[it.Keys.Hourly]; ok {
		parts = append(parts, fmtLabel(v, HourlyScale, "/h"))
	}
	if v, ok := labels[it.Keys.Currency]; ok {
		parts = append(parts, v)
	} else if len(parts) > 0 {
		parts = append(parts, "(no currency: ignored by the reader)")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func fmtLabel(v string, scale int, unit string) string {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return fmt.Sprintf("%q(malformed)", v)
	}
	if scale == MonthlyScale {
		return EURMonth(n) + unit
	}
	return EURHour(n) + unit
}

// pickServer accepts a list number or a server ID.
func (it *Interactive) pickServer(servers []Server) (int, error) {
	for {
		line, err := it.ask("server (# or id, q to quit): ")
		if err != nil {
			return 0, err
		}
		if line == "" {
			continue
		}
		n, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			fmt.Fprintf(it.Out, "  %q is neither a list number nor a server id\n", line)
			continue
		}
		if n >= 1 && int(n) <= len(servers) {
			return int(n) - 1, nil
		}
		for i, s := range servers {
			if s.ID == n {
				return i, nil
			}
		}
		fmt.Fprintf(it.Out, "  no server %d\n", n)
	}
}

// dimension is what the operator chose for one amount label.
type dimension struct {
	set   bool  // write value
	clear bool  // remove the label
	value int64 // minor units, when set
}

func (it *Interactive) editServer(ctx context.Context, s Server, pricing Pricing) (Server, error) {
	fmt.Fprintf(it.Out, "\nserver %d %s (%s in %s)\n", s.ID, s.Name, s.Type, s.Location)
	fmt.Fprintf(it.Out, "  current labels  monthly %s  hourly %s  currency %s\n",
		labelOrDash(s.Labels, it.Keys.Monthly), labelOrDash(s.Labels, it.Keys.Hourly), labelOrDash(s.Labels, it.Keys.Currency))
	fmt.Fprintf(it.Out, "  current override %s\n", it.describe(s.Labels))
	if lp, ok := pricing.ListPrice(s.Type, s.Location); ok {
		fmt.Fprintf(it.Out, "  list price       %s/mo  %s/h  %s net\n",
			EURMonth(lp.MonthlyNetCents), EURHour(lp.HourlyNetMilliCents), pricing.Currency)
	} else {
		fmt.Fprintf(it.Out, "  list price       not in /pricing for %s in %s\n", s.Type, s.Location)
	}
	fmt.Fprintln(it.Out, "enter net amounts as decimals (5.34, 0.0073); empty keeps, - clears, 0 is a price of zero")

	monthly, err := it.askAmount("monthly net", MonthlyScale)
	if err != nil {
		return s, err
	}
	hourly, err := it.askAmount("hourly net", HourlyScale)
	if err != nil {
		return s, err
	}

	want := map[string]string{}
	for k, v := range s.Labels {
		want[k] = v
	}
	applyDim(want, it.Keys.Monthly, monthly)
	applyDim(want, it.Keys.Hourly, hourly)

	_, hasM := want[it.Keys.Monthly]
	_, hasH := want[it.Keys.Hourly]
	if hasM || hasH {
		cur, err := it.askCurrency(pricing.Currency)
		if err != nil {
			return s, err
		}
		want[it.Keys.Currency] = cur
	} else {
		// No amount left: a lone currency label means nothing.
		delete(want, it.Keys.Currency)
	}

	changes := it.diff(s.Labels, want)
	if len(changes) == 0 {
		fmt.Fprintln(it.Out, "nothing to change")
		return s, nil
	}
	fmt.Fprintln(it.Out, "will write:")
	for _, c := range changes {
		fmt.Fprintln(it.Out, "  "+c)
	}
	ok, err := it.confirm("write these labels? [y/N]: ")
	if err != nil {
		return s, err
	}
	if !ok {
		fmt.Fprintln(it.Out, "not written")
		return s, nil
	}

	// PUT replaces the whole label map: start from the labels as they are
	// now, not as they were when the list was fetched, so a label someone
	// added in the meantime survives.
	live, err := it.API.GetServer(ctx, s.ID)
	if err != nil {
		return s, fmt.Errorf("re-read server %d: %w", s.ID, err)
	}
	if live.Type != s.Type || live.Location != s.Location {
		fmt.Fprintf(it.Out, "not written: server is now %s in %s, was %s in %s when you entered the price\n",
			live.Type, live.Location, s.Type, s.Location)
		return live, nil
	}
	final := map[string]string{}
	for k, v := range live.Labels {
		final[k] = v
	}
	for _, k := range it.Keys.All() {
		delete(final, k)
		if v, ok := want[k]; ok {
			final[k] = v
		}
	}
	if err := it.API.SetLabels(ctx, s.ID, final); err != nil {
		return s, fmt.Errorf("server %d (%s): %w", s.ID, s.Name, err)
	}
	fmt.Fprintln(it.Out, "written")
	live.Labels = final
	return live, nil
}

func applyDim(labels map[string]string, key string, d dimension) {
	switch {
	case d.clear:
		delete(labels, key)
	case d.set:
		labels[key] = strconv.FormatInt(d.value, 10)
	}
}

func labelOrDash(labels map[string]string, key string) string {
	if v, ok := labels[key]; ok {
		return strconv.Quote(v)
	}
	return "-"
}

func (it *Interactive) diff(have, want map[string]string) []string {
	var out []string
	for _, k := range it.Keys.All() {
		h, hok := have[k]
		w, wok := want[k]
		switch {
		case hok && !wok:
			out = append(out, fmt.Sprintf("%s: %q -> (removed)", k, h))
		case !hok && wok:
			out = append(out, fmt.Sprintf("%s: (none) -> %q", k, w))
		case hok && wok && h != w:
			out = append(out, fmt.Sprintf("%s: %q -> %q", k, h, w))
		}
	}
	return out
}

func (it *Interactive) askAmount(what string, scale int) (dimension, error) {
	for {
		line, err := it.ask(what + ": ")
		if err != nil {
			return dimension{}, err
		}
		switch line {
		case "":
			return dimension{}, nil
		case "-":
			return dimension{clear: true}, nil
		}
		n, err := ParseMinor(line, scale)
		if err != nil {
			fmt.Fprintln(it.Out, "  "+err.Error())
			continue
		}
		return dimension{set: true, value: n}, nil
	}
}

// askCurrency defaults to, and accepts only, the account currency: the reader ignores an override whose
// currency differs from /pricing, so writing one would change nothing while
// looking like it had.
func (it *Interactive) askCurrency(account string) (string, error) {
	for {
		line, err := it.ask(fmt.Sprintf("currency (EUR/USD) [%s]: ", account))
		if err != nil {
			return "", err
		}
		cur := strings.ToUpper(line)
		if cur == "" {
			cur = account
		}
		if cur != "EUR" && cur != "USD" {
			fmt.Fprintf(it.Out, "  %q: the currency must be EUR or USD\n", line)
			continue
		}
		if cur != account {
			fmt.Fprintf(it.Out, "  this account is billed in %s; an override in %s would be ignored by the reader\n", account, cur)
			continue
		}
		return cur, nil
	}
}

func (it *Interactive) confirm(prompt string) (bool, error) {
	line, err := it.ask(prompt)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// ask prints a prompt and reads one trimmed line. "q" and end of input quit.
func (it *Interactive) ask(prompt string) (string, error) {
	fmt.Fprint(it.Out, prompt)
	line, err := it.In.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(it.Out)
			return "", errQuit
		}
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "q" || line == "quit" {
		return "", errQuit
	}
	return line, nil
}
