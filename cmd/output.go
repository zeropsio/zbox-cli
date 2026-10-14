package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"golang.org/x/term"

	"github.com/zeropsio/zbox-cli/internal/api"
)

// The output contract is in docs/cli-ux.md: a bordered table on a terminal, tab-separated values when stdout is not one, the API objects under --json, ids only under --quiet.

var (
	stdoutTerminal = term.IsTerminal(int(os.Stdout.Fd()))
	stdinTerminal  = term.IsTerminal(int(os.Stdin.Fd()))
)

// pretty is true when a person reads stdout: borders, relative times, prose. Anything else gets the plain form a script or an agent parses.
func pretty() bool {
	return stdoutTerminal && !flagJson
}

// grid is one table of a listing. The first column is the key a --quiet run prints, unless keyCol says otherwise.
type grid struct {
	header []string
	rows   [][]string
	key    int
	empty  string
}

func newTable(header ...string) *grid {
	return &grid{header: header}
}

// row takes the cells in column order; anything that is not a string is printed with fmt.Sprint, so the API's enums go in as they are.
func (g *grid) row(cells ...any) {
	r := make([]string, len(cells))
	for i, c := range cells {
		r[i] = fmt.Sprint(c)
	}
	g.rows = append(g.rows, r)
}

// keyCol names the column --quiet prints when it is not the first.
func (g *grid) keyCol(i int) *grid {
	g.key = i
	return g
}

// ifEmpty is the hint printed to stderr instead of an empty table, saying what to do about it.
func (g *grid) ifEmpty(format string, args ...any) *grid {
	g.empty = fmt.Sprintf(format, args...)
	return g
}

func (g *grid) print() {
	if flagQuiet {
		for _, r := range g.rows {
			fmt.Println(r[g.key])
		}
		return
	}
	if len(g.rows) == 0 {
		if g.empty != "" {
			note("%s", g.empty)
		}
		return
	}
	if !pretty() {
		fmt.Println(strings.Join(g.header, "\t"))
		for _, r := range g.rows {
			fmt.Println(plainRow(r))
		}
		return
	}
	t := asciiTable()
	t.Header(g.header)
	for _, r := range g.rows {
		_ = t.Append(r)
	}
	_ = t.Render()
}

// fields prints one object as label and value pairs: a two-column table on a terminal, label<TAB>value lines otherwise. The first value is the object's key, the one --quiet prints.
func fields(pairs ...string) {
	if flagQuiet {
		if len(pairs) > 1 {
			fmt.Println(pairs[1])
		}
		return
	}
	if !pretty() {
		for i := 0; i+1 < len(pairs); i += 2 {
			fmt.Println(plainRow([]string{pairs[i], pairs[i+1]}))
		}
		return
	}
	t := asciiTable()
	for i := 0; i+1 < len(pairs); i += 2 {
		_ = t.Append([]string{pairs[i], pairs[i+1]})
	}
	_ = t.Render()
}

// asciiTable is the bordered table of a terminal: +---+ borders, cells as given (no wrapping, no case change, no trimming, a line break in a cell stacks lines).
func asciiTable() *tablewriter.Table {
	cfg := tablewriter.NewConfigBuilder().
		WithRowAutoWrap(tw.WrapNone).
		WithRowAlignment(tw.AlignLeft).
		WithHeaderAlignment(tw.AlignCenter).
		WithHeaderAutoFormat(tw.Off).
		WithTrimSpace(tw.Off).
		Build()
	r := renderer.NewBlueprint(tw.Rendition{Symbols: tw.NewSymbols(tw.StyleASCII)})
	return tablewriter.NewTable(os.Stdout, tablewriter.WithConfig(cfg), tablewriter.WithRenderer(r))
}

var cellBreaks = strings.NewReplacer("\t", " ", "\r\n", " ", "\n", " ")

// plainRow joins cells with tabs; a tab or a line break inside a cell would split it, so they become spaces.
func plainRow(cells []string) string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = cellBreaks.Replace(c)
	}
	return strings.Join(out, "\t")
}

// section prints a titled block under a detail view (a policy, a result object) as indented JSON. --quiet leaves it out.
func section(title string, v any) {
	if flagQuiet {
		return
	}
	if !pretty() {
		// Piped, it stays a label<TAB>value line like the fields above it, the value compact JSON.
		data, _ := json.Marshal(v)
		fmt.Println(title + "\t" + string(data))
		return
	}
	data, _ := json.MarshalIndent(v, "  ", "  ")
	fmt.Println(title + ":")
	fmt.Println("  " + string(data))
}

// say prints a line of the command's result for a person, for example `Policy of x updated`. --quiet drops it, --json never reaches it.
func say(format string, args ...any) {
	if flagQuiet {
		return
	}
	fmt.Printf(format+"\n", args...)
}

// result is the line a mutation prints per object: the id alone under --quiet, the sentence otherwise.
func result(id, format string, args ...any) {
	if flagQuiet {
		fmt.Println(id)
		return
	}
	fmt.Printf(format+"\n", args...)
}

// note goes to stderr: progress, hints, what to do next. Not under --json or --quiet, whose stdout a caller may read together with stderr.
func note(format string, args ...any) {
	if flagQuiet || flagJson {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// when is a timestamp of the API: `3m ago (2026-10-09 14:02:11)` in local time for a person, RFC 3339 in UTC for a parser.
func when(s string) string {
	t := api.Time(s)
	if t.IsZero() {
		return dash(s)
	}
	if !pretty() {
		return t.UTC().Format(time.RFC3339)
	}
	return relative(t) + " (" + t.Local().Format(time.DateTime) + ")"
}

func whenPtr(p *string) string {
	if p == nil {
		return "-"
	}
	return when(*p)
}

// relative is a coarse distance from now, `3m ago` or `in 2h`; the absolute time beside it carries the precision.
// coarse is an age at the resolution of the AGE columns: 42s, 3m, 2h, 5d. It is for how long ago something happened or how long it has been going, never for a measured span, which durationString prints.
func coarse(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func relative(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	future := d < 0
	if future {
		d = -d
	}
	s := coarse(d)
	if future {
		return "in " + s
	}
	return s + " ago"
}
