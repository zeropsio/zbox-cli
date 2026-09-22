// Package cmd holds the cobra commands of the zbox CLI.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
	"github.com/zeropsio/zbox-cli/internal/config"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

var (
	flagApiUrl string
	flagGuiUrl string
	flagJson   bool
)

var rootCmd = &cobra.Command{
	Use:           "zbox",
	Short:         "Agent sandboxes on Zerops",
	Long:          "zbox creates a sandbox workspace in your Zerops account and manages the sandboxes inside it.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		var exit exitError
		if errors.As(err, &exit) {
			os.Exit(exit.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return err
	}
	return nil
}

// exitError carries a remote exit code (zbox exec) to the process exit status without printing anything.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

func init() {
	pf := rootCmd.PersistentFlags()
	pf.BoolVar(&flagJson, "json", false, "print the API objects as JSON instead of text, for scripts and agents")
	pf.StringVar(&flagApiUrl, "public-api-url", "", "Zerops public API base URL, stored by `zbox login`")
	pf.StringVar(&flagGuiUrl, "gui-url", "", "Zerops GUI origin for the browser hand-offs, overrides guiUrl from the config")
	_ = pf.MarkHidden("public-api-url")
	_ = pf.MarkHidden("gui-url")
}

func loadConfig() (config.Config, error) {
	c, err := config.Load()
	if err != nil {
		return c, err
	}
	if flagApiUrl != "" {
		c.ApiUrl = flagApiUrl
	}
	if flagGuiUrl != "" {
		c.GuiUrl = flagGuiUrl
	}
	return c, nil
}

func zeropsClient(c config.Config) (*zerops.Client, error) {
	token := c.EffectiveToken()
	if token == "" {
		return nil, errors.New("not logged in, run `zbox login` first")
	}
	return zerops.New(c.ApiUrl, token), nil
}

// workspace is the orchestrator client of the assigned workspace. Most commands start here.
func workspace() (*api.Workspace, error) {
	c, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if !c.HasWorkspace() {
		return nil, errors.New("no workspace yet, run `zbox setup` or `zbox workspace use <url> --api-key <key>`")
	}
	url, key := c.EffectiveWorkspace()
	workspaceKey = key
	return api.New(url, key), nil
}

// workspaceKey is the key of the workspace in use, printed into MEMBERS port URLs (see openUrl).
var workspaceKey string

// ---- output

// emit prints v as JSON under --json and runs the text printer otherwise, so every command has both forms from one place.
func emit(v any, text func()) error {
	if flagJson {
		return printJson(v)
	}
	text()
	return nil
}

func printJson(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// table writes a header and rows aligned in columns.
func table(header string, rows func(w io.Writer)) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, header)
	rows(w)
	_ = w.Flush()
}

// fields prints label/value pairs aligned, the text form of one object.
func fields(pairs ...string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	for i := 0; i+1 < len(pairs); i += 2 {
		fmt.Fprintf(w, "%s\t%s\n", pairs[i]+":", pairs[i+1])
	}
	_ = w.Flush()
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func when(s string) string {
	t := api.Time(s)
	if t.IsZero() {
		return dash(s)
	}
	return t.Local().Format(time.RFC3339)
}

func whenPtr(p *string) string {
	if p == nil {
		return "-"
	}
	return when(*p)
}

func age(s string) string {
	t := api.Time(s)
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t).Round(time.Second)
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

func short(digest string) string {
	s := strings.TrimPrefix(digest, "sha256:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func bytesString(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/unit >= div && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

func bytesPtr(p *int64) string {
	if p == nil {
		return "-"
	}
	return bytesString(*p)
}

// ---- input

// parseBytes reads a size like 512m, 2g or a plain number of bytes.
func parseBytes(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "g"), strings.HasSuffix(s, "gb"):
		mult = 1 << 30
	case strings.HasSuffix(s, "m"), strings.HasSuffix(s, "mb"):
		mult = 1 << 20
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "kb"):
		mult = 1 << 10
	}
	s = strings.TrimRight(s, "bgmk")
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size, use for example 512m or 2g", s)
	}
	return n * mult, nil
}

// seconds turns a duration flag (30m, 2h, 90s) into the seconds the API takes, nil when the flag was not given.
func seconds(d time.Duration) *int {
	if d == 0 {
		return nil
	}
	n := int(d / time.Second)
	return &n
}

// readJson reads one JSON object from a file, or from stdin when the path is "-".
func readJson(path string, out any) error {
	var r io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func upper[T ~string](s string) T {
	return T(strings.ToUpper(strings.ReplaceAll(s, "-", "_")))
}

func upperPtr[T ~string](s string) *T {
	if s == "" {
		return nil
	}
	v := upper[T](s)
	return &v
}
