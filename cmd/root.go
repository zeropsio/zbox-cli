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
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
	"github.com/zeropsio/zbox-cli/internal/config"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

var (
	flagApiUrl    string
	flagGuiUrl    string
	flagWorkspace string
	flagJson      bool
	flagQuiet     bool
)

var rootCmd = &cobra.Command{
	Use:   "zbox",
	Short: "Agent sandboxes on Zerops",
	Long: `zbox creates a sandbox workspace in your Zerops account and manages the sandboxes inside it.

Output: a table on a terminal, tab-separated values with a header line when stdout is a pipe,
the API objects as JSON with --json, only the ids with --quiet. Progress and hints go to stderr.
Errors under --json are one JSON object on stderr: {"error":{"code","message","status","meta","exitCode"}}.

Exit codes: 0 ok, 1 failed, 2 usage, 3 not found, 4 conflict (state, name, an operation in flight),
5 timed out, 6 not authorized, 7 unavailable (retry later), 130 interrupted.
zbox exec exits with the command's own code, and with 125 when zbox itself failed.

Sandboxes are named by id or name everywhere. Destructive commands ask on a terminal and need --yes otherwise.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	markUsage(rootCmd)
	c, err := rootCmd.ExecuteContextC(ctx)
	if err == nil {
		return nil
	}
	var exit exitError
	if errors.As(err, &exit) {
		os.Exit(exit.code)
	}
	if ctx.Err() != nil {
		err = interrupted{}
	}
	code := exitCode(err)
	// zbox exec passes the command's exit code through, so its own failures take docker's 125 and cannot be mistaken for the command's.
	if c == execCmd && code != exitUsage && code != exitTimeout && code != exitInterrupted {
		code = exitExecFailed
	}
	reportError(err, code)
	os.Exit(code)
	return err
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.BoolVar(&flagJson, "json", false, "print the API objects as JSON instead of text, for scripts and agents")
	pf.BoolVarP(&flagQuiet, "quiet", "q", false, "print only the ids of what was listed, created or changed, one per line")
	pf.StringVar(&flagWorkspace, "workspace", "", "work on this known workspace for one run and leave the current one as it is: its name, service id, project name or API host (env "+config.EnvWorkspace+")")
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

// workspaceRef is the workspace named for this run and where the name came from, the flag before ZBOX_WORKSPACE.
func workspaceRef() (ref, source string) {
	if flagWorkspace != "" {
		return flagWorkspace, "--workspace"
	}
	return os.Getenv(config.EnvWorkspace), config.EnvWorkspace
}

// loadSelected is the config of the commands that work on a workspace: the workspace named for this run stands in for the current one (config.Pin), and saving such a config keeps the stored selection. The commands that manage the selection itself (switch, use, authorize, setup, ls) read loadConfig.
func loadSelected() (config.Config, error) {
	c, err := loadConfig()
	if err != nil {
		return c, err
	}
	ref, source := workspaceRef()
	if ref == "" {
		return c, nil
	}
	// The flag is the last word, but two env variables naming a workspace leave no way to tell which one was meant.
	if source == config.EnvWorkspace && (os.Getenv(config.EnvWorkspaceUrl) != "" || os.Getenv(config.EnvWorkspaceKey) != "") {
		return c, usagef("%s and %s/%s both name the workspace, unset one of them", config.EnvWorkspace, config.EnvWorkspaceUrl, config.EnvWorkspaceKey)
	}
	c, err = c.Pin(ref)
	if err != nil {
		return c, fmt.Errorf("%s: %w", source, err)
	}
	return c, nil
}

// refuseOverride guards the commands whose whole effect is to change the current workspace, which a workspace named for one run is there to prevent.
func refuseOverride(command string) error {
	if ref, source := workspaceRef(); ref != "" {
		return usagef("`%s` changes the current workspace and %s (%s) names one for a single run, run it without", command, source, ref)
	}
	return nil
}

func zeropsClient(c config.Config) (*zerops.Client, error) {
	token := c.EffectiveToken()
	if token == "" {
		return nil, notAuthorized{"not logged in, run `zbox login` first"}
	}
	return zerops.New(c.ApiUrl, token), nil
}

// workspace is the orchestrator client of the assigned workspace. Most commands start here.
func workspace() (*api.Workspace, error) {
	c, err := loadSelected()
	if err != nil {
		return nil, err
	}
	if !c.HasWorkspace() {
		return nil, notAuthorized{"no workspace yet, run `zbox setup` or `zbox workspace use <url> --api-key <key>`"}
	}
	url, key := c.EffectiveWorkspace()
	return api.New(url, key), nil
}

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

// firstLine keeps a table cell on one row: a build failure carries the build log, `operation get` prints all of it.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// took is the span between two API timestamps at millisecond resolution: whole seconds hide that a wake takes 400 ms and a park 3 s (the user's rule of 2026-09-22, "1s is too high of resolution"). An unfinished span is a dash.
func took(created string, finished *string) string {
	if finished == nil {
		return "-"
	}
	from, to := api.Time(created), api.Time(*finished)
	if from.IsZero() || to.IsZero() {
		return "-"
	}
	return durationString(to.Sub(from))
}

func durationString(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	m := int(d / time.Minute)
	return fmt.Sprintf("%dm %.1fs", m, (d - time.Duration(m)*time.Minute).Seconds())
}

// tookNote is the " in 395 ms" tail of a finished operation's line.
func tookNote(op *api.BaseOperation) string {
	if op == nil || op.Finished == nil {
		return ""
	}
	return " in " + took(op.Created, op.Finished)
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
		return 0, usagef("%q is not a size, use for example 512m or 2g", s)
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
		return usageError{fmt.Errorf("%s: %w", path, err)}
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
