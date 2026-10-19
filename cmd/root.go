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

// betaNotice marks every zbox surface while Zerops Box ships as a beta (v0): the root help, the README and one stderr line of `zbox workspace status`. Visual only, nothing else changes.
const betaNotice = "zbox and Zerops Box are in beta: behaviour, commands, flags and the API can change without notice"

// version is the release tag the binary was built from, set by the release workflow through -ldflags "-X github.com/zeropsio/zbox-cli/cmd.version=v0.24"; a local build says dev.
var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "zbox",
	Short: "Agent sandboxes on Zerops",
	Long: `zbox creates a Zerops Box workspace in your Zerops account and manages the sandboxes inside it.

Output: a table on a terminal, tab-separated values with a header line when stdout is a pipe,
the API objects as JSON with --json, only the ids with --quiet. Progress and hints go to stderr.
Errors under --json are one JSON object on stderr: {"error":{"code","message","status","meta","exitCode"}}.

Exit codes: 0 ok, 1 failed, 2 usage, 3 not found, 4 conflict (state, name, an operation in flight),
5 timed out, 6 not authorized, 7 unavailable (retry later), 130 interrupted.
zbox exec exits with the command's own code, and with 125 when zbox itself failed.

Sandboxes are named by id or name everywhere. Destructive commands ask on a terminal and need --yes otherwise.

` + betaNotice + `.`,
	Version:       version,
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
	pf.StringVar(&flagGuiUrl, "gui-url", "", "Zerops GUI origin that serves the login consent page in place of the one the API names, overrides guiUrl from the config")
	_ = pf.MarkHidden("public-api-url")
	_ = pf.MarkHidden("gui-url")
}

func loadConfig() (config.Config, error) {
	c, err := config.Load()
	if err != nil {
		return c, err
	}
	// The hidden flags hold for this run and are not saved by a refresh or an access mint on the way; `zbox login` adopts them, since storing them is its job. An empty --gui-url is a value too: it brings the API's own consent page back after a spell on the dev server.
	var guiUrl *string
	if rootCmd.PersistentFlags().Changed("gui-url") {
		guiUrl = &flagGuiUrl
	}
	c.Override(flagApiUrl, guiUrl)
	if set, missing := config.HalfEnvPair(); set != "" {
		return c, usagef("%s is set without %s, the pair names a workspace only as a whole", set, missing)
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

// loadSelected is the config of the commands that work on a workspace: the workspace named for this run stands in for the current one (config.Pin), and saving such a config keeps the stored selection. The commands that manage the selection itself (switch, use, authorize, setup, list) read loadConfig.
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

// zeropsClient is the public API client of the login. A session about to run out is refreshed first and saved, through the pointer the caller keeps using.
func zeropsClient(ctx context.Context, c *config.Config) (*zerops.Client, error) {
	if err := refreshSession(ctx, c); err != nil {
		return nil, err
	}
	token := c.EffectiveToken()
	if token == "" {
		return nil, notAuthorized{"not logged in, run `zbox login` first"}
	}
	return zerops.New(c.ApiUrl, token), nil
}

// sessionMargin is how much of the access token's life has to be left for a run to start on it.
const sessionMargin = time.Minute

// refreshSession renews the session when its access token has under sessionMargin left. ZBOX_TOKEN leaves the stored session alone.
// A refresh kills the old pair, and another zbox run may have refreshed since this one loaded the config, so the file is read again before and after a refusal.
func refreshSession(ctx context.Context, c *config.Config) error {
	if os.Getenv(config.EnvToken) != "" || c.Session == nil || time.Until(c.Session.ExpiresAt) > sessionMargin {
		return nil
	}
	if fresh := storedSession(c.Session); fresh != nil {
		c.Session = fresh
		return nil
	}
	s, err := zerops.Refresh(ctx, c.ApiUrl, c.Session.RefreshToken)
	if err != nil {
		if fresh := storedSession(c.Session); fresh != nil {
			c.Session = fresh
			return nil
		}
		if zerops.IsCode(err, "refreshTokenInvalid") {
			return notAuthorized{"the Zerops login ran out, run `zbox login` again"}
		}
		return fmt.Errorf("refresh the Zerops login: %w", err)
	}
	c.Session = &config.Session{AccessToken: s.AccessToken, ExpiresAt: s.ExpiresAt, RefreshToken: s.RefreshToken}
	return c.Save()
}

// storedSession is the session in the file when it is a newer one than known and still good, nil otherwise.
func storedSession(known *config.Session) *config.Session {
	stored, err := config.Load()
	if err != nil || stored.Session == nil || stored.Session.AccessToken == known.AccessToken || time.Until(stored.Session.ExpiresAt) <= sessionMargin {
		return nil
	}
	return stored.Session
}

// noWorkspaceHint is the answer of every command that needs a workspace when none is in use.
const noWorkspaceHint = "no workspace yet, run `zbox setup`, `zbox workspace authorize <service-id>` for one of the account, or `zbox workspace use <url> --api-key <key>`"

// workspace is the orchestrator client of the assigned workspace. Most commands start here.
func workspace() (*api.Workspace, error) {
	c, err := loadSelected()
	if err != nil {
		return nil, err
	}
	return connect(context.Background(), &c)
}

// connect is the orchestrator client of the workspace in use. A workspace of the account is reached through zproxy: the stored access is used, minted first when there is none, and minted again by the client when zproxy says it expired (api.Workspace.Renew); each fresh access is saved. A workspace pointed at by hand is reached at its own API host with its key.
func connect(ctx context.Context, c *config.Config) (*api.Workspace, error) {
	w := c.EffectiveWorkspace()
	if !w.Usable() {
		return nil, notAuthorized{noWorkspaceHint}
	}
	if w.Direct() {
		return api.New(w.WorkspaceUrl, w.ApiKey), nil
	}
	zc, err := zeropsClient(ctx, c)
	if err != nil {
		// Only a missing or dead login gets the sentence; a refresh that failed on the network keeps its class, so a script does not log in again for a timeout.
		if errors.As(err, new(notAuthorized)) {
			return nil, notAuthorized{fmt.Sprintf("%v: workspace %s is reached through the Zerops account", err, w.Name)}
		}
		return nil, err
	}
	mint := func(ctx context.Context) (string, error) {
		a, err := mintAccess(ctx, c, zc, w)
		if err != nil {
			return "", err
		}
		return a.Token, nil
	}
	var access config.Access
	if w.Access != nil {
		access = *w.Access
	}
	if access.Token == "" || access.Url == "" {
		if access, err = mintAccess(ctx, c, zc, w); err != nil {
			return nil, err
		}
	}
	ws := api.New(access.Url, access.Token)
	ws.Renew = mint
	return ws, nil
}

// mintAccess asks the public API for a zproxy access to the workspace service and stores it. The errors a reader can act on get a sentence: a stopped workspace, a read-only role, a workspace deleted elsewhere.
func mintAccess(ctx context.Context, c *config.Config, zc *zerops.Client, w config.Workspace) (config.Access, error) {
	a, err := zc.UiAccess(ctx, w.ServiceId)
	if err != nil {
		switch {
		case zerops.IsCode(err, "serviceStackNoActiveContainer"):
			return config.Access{}, unavailable{fmt.Sprintf("workspace %s (%s) is not running, start the service in the Zerops GUI", w.Name, w.Label())}
		case zerops.IsCode(err, "insufficientPermissions"):
			return config.Access{}, notAuthorized{fmt.Sprintf("the account has a read-only role on workspace %s (%s), reaching it needs BasicUser or above", w.Name, w.Label())}
		case zerops.IsNotFound(err):
			return config.Access{}, notFoundf("workspace %s (%s) is gone from the account, `zbox workspace forget` drops it here", w.Name, w.Label())
		}
		return config.Access{}, fmt.Errorf("mint the access to workspace %s: %w", w.Name, err)
	}
	access := config.Access{Url: a.UrlApi, Token: a.Token}
	if err := c.SaveAccess(w, access); err != nil {
		return config.Access{}, err
	}
	return access, nil
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
