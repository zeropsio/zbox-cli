package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
	"github.com/zeropsio/zbox-cli/internal/config"
	"github.com/zeropsio/zbox-cli/internal/waiting"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

const (
	defaultHostname = "zbox"
	// defaultBaseImage is the base a workspace gets when neither --base nor --build names one, pulled by the orchestrator from the tarball URL.
	defaultBaseImage = config.DefaultBaseImage
	// The service env the Box Workspace controller generates the workspace API key into at service create. The CLI never stores it, zproxy holds it; `zbox workspace key` reads it for the user.
	apiKeyEnv = "ZBOX_WORKSPACE_API_KEY"
	// How long a new service gets to turn ACTIVE with its container after the import's processes finished, before the access can be minted.
	accessTimeout = 2 * time.Minute
)

var (
	flagSetupName      string
	flagSetupProject   string
	flagSetupProjectId string
	flagSetupBases     []string
	flagSetupBuilds    []string
	flagSetupTemplates []string
	flagSetupWarmups   []string
	flagSetupCpu       int
	flagSetupRam       int
	flagSetupDisk      int
	flagSetupNoSubdom  bool
	setupPingTimeout   = 5 * time.Minute
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Create a workspace, in a new Zerops project or in an existing one",
	Long: `Creates the workspace service --name (in a new project --project, or in the one --project-id names), waits for it and stores it here.
The orchestrator is reached through zproxy with an access minted from the Zerops login, no key is stored. Then, in this order: every --build is
uploaded and built into a base, every --base is pulled, and every --template is warmed on the base it names. Without --build and --base the
default sandbox image is pulled. Subdomain access is on unless --no-subdomain: it gives the sandbox ports their public URLs.`,
	Example: `  zbox setup
  zbox setup --name zbox --build ./image --template dev=2:4g@image`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := refuseOverride("zbox setup"); err != nil {
			return err
		}
		c, err := loadConfig()
		if err != nil {
			return err
		}
		// Several workspaces are fine, they are told apart by name; the new one becomes the current one.
		if w, err := c.Lookup(flagSetupName); err == nil && w.Name == flagSetupName {
			return fmt.Errorf("a workspace named %s is already known (%s), pick another --name or run `zbox workspace teardown` or `zbox workspace forget` on it first", w.Name, w.Label())
		}
		return createWorkspace(cmd.Context(), &c)
	},
}

// setupBuild is the --build context, packed before anything is created so a missing Dockerfile fails the command at once.
type setupBuild struct {
	name string
	body io.ReadCloser
	size int64
}

// buildName is what a build is called when name= says nothing: the context directory's name, or the archive's without its extension.
func buildName(source string) string {
	abs, err := filepath.Abs(source)
	if err != nil {
		abs = source
	}
	name := filepath.Base(abs)
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		name = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".zip"), ".gz"), ".tar")
	}
	return name
}

// createWorkspace brings the workspace up, stores it and runs the chain of builds, pulls and templates. It is shared with `zbox new`, which offers the setup when nothing is assigned yet.
func createWorkspace(ctx context.Context, c *config.Config) error {
	zc, err := zeropsClient(ctx, c)
	if err != nil {
		return err
	}
	templates, err := setupTemplates(flagSetupTemplates, flagSetupWarmups)
	if err != nil {
		return err
	}
	builds, err := setupBuilds(flagSetupBuilds)
	if err != nil {
		return err
	}
	defer func() {
		for _, b := range builds {
			_ = b.body.Close()
		}
	}()
	pulls, err := setupPulls(flagSetupBases)
	if err != nil {
		return err
	}
	if len(builds) == 0 && len(pulls) == 0 {
		pulls = []setupPull{{ref: defaultBaseImage}}
	}
	if err := checkTemplateBases(templates, builds, pulls); err != nil {
		return err
	}
	// The screen prints the stages and, on a TTY, a note about workspaces every few seconds, so the minutes of the build do not look like a hang.
	screen := waiting.New()
	defer screen.Stop()

	ws, err := newWorkspace(ctx, c, zc, screen)
	if err != nil {
		return err
	}

	// The bases in the order the help promises: builds, then pulls. byName is what a template's @base resolves against.
	var bases []*api.BaseBase
	byName := map[string]*api.BaseBase{}
	for _, b := range builds {
		screen.Step("Uploading the build context and building the base " + b.name)
		base, err := buildSetupBase(ctx, ws, b)
		if err != nil {
			return err
		}
		bases = append(bases, base)
		byName[b.name] = base
	}
	for _, p := range pulls {
		screen.Step("Pulling the base " + cmp.Or(p.name, p.ref))
		op, err := ws.CreateBase(ctx, api.RequestPostBase{ImageRef: p.ref, Name: optional(p.name)})
		if err != nil {
			return err
		}
		if op, err = ws.WaitOperation(ctx, op.Id); err != nil {
			return err
		}
		base, err := ws.BaseOf(ctx, op)
		if err != nil {
			return err
		}
		bases = append(bases, base)
		byName[p.ref] = base
		if p.name != "" {
			byName[p.name] = base
		}
	}
	// Templates are not built at start any more (since 2026-10-06), they are created through the API the way `zbox template create --warm` does, one after the other, each waited for.
	for _, t := range templates {
		base := byName[t.Base]
		screen.Step("Building the warm template " + t.Name + " on " + baseLabel(base))
		if err := createSetupTemplate(ctx, ws, base.Digest, t); err != nil {
			screen.Stop()
			printSetupDone(*c, bases, nil)
			return fmt.Errorf("%w; the workspace is up, `zbox template create --warm` builds the template again", err)
		}
	}
	screen.Stop()
	printSetupDone(*c, bases, templates)
	return nil
}

// printSetupDone is the result of a setup: the URL is the key --quiet prints, the rest is what was built and what to do next.
func printSetupDone(c config.Config, bases []*api.BaseBase, templates []setupTemplate) {
	w, _ := c.Current()
	if w.WorkspaceUrl != "" {
		result(w.Name, "The workspace %s is ready: %s, reached through zproxy.", w.Name, w.WorkspaceUrl)
	} else {
		result(w.Name, "The workspace %s is ready, reached through zproxy. Without subdomain access the sandbox ports have no public URL.", w.Name)
	}
	for _, b := range bases {
		say("Base %s (%s).", short(b.Digest), baseLabel(b))
	}
	if len(templates) > 0 {
		names := make([]string, len(templates))
		for i, t := range templates {
			names[i] = t.Name
		}
		say("Warm templates: %s.", strings.Join(names, ", "))
	}
	note("Create your first sandbox with `zbox new`, open the console with `zbox workspace console`, or find the workspace in the Zerops GUI.")
}

// setupPull is one --base, [name=]ref.
type setupPull struct {
	name string
	ref  string
}

func setupPulls(specs []string) ([]setupPull, error) {
	var out []setupPull
	for _, spec := range specs {
		name, ref := splitNamed(spec)
		if ref == "" {
			return nil, fmt.Errorf("--base %q: use [name=]<image ref or tarball URL>", spec)
		}
		out = append(out, setupPull{name: name, ref: ref})
	}
	return out, nil
}

// setupBuilds packs every --build, [name=]<dir|archive>, before anything is created, so a missing Dockerfile fails the command at once.
func setupBuilds(specs []string) ([]*setupBuild, error) {
	var out []*setupBuild
	seen := map[string]bool{}
	closeAll := func() {
		for _, b := range out {
			_ = b.body.Close()
		}
	}
	for _, spec := range specs {
		name, source := splitNamed(spec)
		if source == "" {
			closeAll()
			return nil, fmt.Errorf("--build %q: use [name=]<directory or archive>", spec)
		}
		name = cmp.Or(name, buildName(source))
		if seen[name] {
			closeAll()
			return nil, fmt.Errorf("--build %q: a build is already named %s, give it a name as name=%s", spec, name, source)
		}
		seen[name] = true
		body, size, report, err := buildContext(source)
		if err != nil {
			closeAll()
			return nil, err
		}
		if flagBuildVerbose {
			fmt.Fprintf(os.Stderr, "%s:\n", name)
		} else {
			fmt.Fprintf(os.Stderr, "%s: ", name)
		}
		report.print(os.Stderr, flagBuildVerbose)
		out = append(out, &setupBuild{name: name, body: body, size: size})
	}
	return out, nil
}

// splitNamed reads a [name=]value flag. An image ref or URL never has `=` before its first `/`, `:` or `.`, so only a plain word before `=` is a name.
func splitNamed(spec string) (string, string) {
	if i := strings.IndexByte(spec, '='); i > 0 && !strings.ContainsAny(spec[:i], "/:.") {
		return spec[:i], spec[i+1:]
	}
	return "", spec
}

// checkTemplateBases refuses a template whose base names neither a build nor a pull, and a pull named like a build, before anything is created.
func checkTemplateBases(templates []setupTemplate, builds []*setupBuild, pulls []setupPull) error {
	known := map[string]bool{}
	for _, b := range builds {
		known[b.name] = true
	}
	for _, p := range pulls {
		if known[p.name] || known[p.ref] {
			return fmt.Errorf("--base %s is named like a --build, give it another name", cmp.Or(p.name, p.ref))
		}
		known[p.ref] = true
		if p.name != "" {
			known[p.name] = true
		}
	}
	for _, t := range templates {
		if !known[t.Base] {
			return fmt.Errorf("--template %s names the base %q, which no --build or --base provides", t.Name, t.Base)
		}
	}
	return nil
}

func baseLabel(b *api.BaseBase) string {
	if b.Name != nil {
		return *b.Name + ", " + b.ImageRef
	}
	return b.ImageRef
}

// newWorkspace imports the service, stores it, mints its zproxy access and waits for the orchestrator to answer. The import names no base, the chain registers them through the API.
func newWorkspace(ctx context.Context, c *config.Config, zc *zerops.Client, screen *waiting.Screen) (*api.Workspace, error) {
	var res zerops.ImportResult
	if flagSetupProjectId != "" {
		// The service-stack import carries only the services section, the project is the one named by the flag.
		yaml, err := zerops.RenderImport(zerops.ImportParams{Hostname: flagSetupName, Cpu: flagSetupCpu, Ram: flagSetupRam, Disk: flagSetupDisk, NoSubdomain: flagSetupNoSubdom})
		if err != nil {
			return nil, err
		}
		note("Creating the workspace %s in project %s.", flagSetupName, flagSetupProjectId)
		screen.Start()
		screen.Step("Creating the workspace")
		res, err = zc.ImportServiceStack(ctx, flagSetupProjectId, yaml)
		if err != nil {
			return nil, err
		}
	} else {
		user, err := zc.UserInfo(ctx)
		if err != nil {
			return nil, err
		}
		yaml, err := zerops.RenderImport(zerops.ImportParams{ProjectName: flagSetupProject, Hostname: flagSetupName, Cpu: flagSetupCpu, Ram: flagSetupRam, Disk: flagSetupDisk, NoSubdomain: flagSetupNoSubdom})
		if err != nil {
			return nil, err
		}
		note("Creating project %s with the workspace %s in %s.", flagSetupProject, flagSetupName, user.Account)
		screen.Start()
		screen.Step("Creating the project")
		res, err = zc.ImportProject(ctx, user.ClientId, yaml)
		if err != nil {
			return nil, err
		}
		c.ClientId = user.ClientId
	}
	// The workspace is stored as soon as the service exists, so a setup that fails from here on can still be torn down.
	w := config.Workspace{Name: flagSetupName, ProjectId: res.ProjectId, ProjectName: res.ProjectName, ServiceId: res.ServiceId}
	if err := c.Assign(w); err != nil {
		return nil, err
	}
	screen.Step("Starting the workspace")
	// The Zerops processes behind the import are not shown, their names describe the plumbing rather than anything the reader can act on, and a failure is reported as an error anyway.
	if err := zc.WaitProcesses(ctx, res.Processes, func(zerops.ProcessState) {}); err != nil {
		return nil, err
	}
	screen.Step("Minting the access to the workspace")
	// The access comes from the public API, nothing is read from the service env and no key is stored here. The container turns ACTIVE a moment after the processes finish, a mint before that answers serviceStackNoActiveContainer and is tried again.
	if err := waitForAccess(ctx, c, zc, w); err != nil {
		return nil, err
	}
	ws, err := connect(ctx, c)
	if err != nil {
		return nil, err
	}
	screen.Step("Waiting for the orchestrator to answer")
	if err := waitForPing(ctx, ws); err != nil {
		return nil, err
	}
	// The two subdomain hosts come from the service env the platform writes: the API host is kept for the listings and for `zbox workspace switch`, the ingress host
	// becomes the workspace's first domain, where the sandbox ports get their URLs. A --no-subdomain workspace has neither. The entry in the list carries the access by now, so it is the one updated.
	apiHost, ingressHost := subdomainHosts(ctx, zc, w.ServiceId)
	if apiHost != "" {
		if cur, ok := c.Current(); ok {
			cur.WorkspaceUrl = "https://" + apiHost
			if err := c.Assign(cur); err != nil {
				return nil, err
			}
		}
	}
	if ingressHost != "" {
		screen.Step("Publishing sandbox ports under " + ingressHost)
		if _, err := ws.PutWorkspaceDomains(ctx, []api.BaseWorkspaceDomain{{Domain: ingressHost, Form: api.DASH}}); err != nil {
			return nil, err
		}
	}
	return ws, nil
}

// waitForAccess mints the first access of a new workspace, waiting out the moment between the import's processes and the container turning ACTIVE.
func waitForAccess(ctx context.Context, c *config.Config, zc *zerops.Client, w config.Workspace) error {
	deadline := time.Now().Add(accessTimeout)
	for {
		_, err := mintAccess(ctx, c, zc, w)
		if err == nil {
			return nil
		}
		if !errors.As(err, new(unavailable)) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// buildSetupBase posts the context and waits for the BUILD_BASE operation, the way `zbox base build` does.
func buildSetupBase(ctx context.Context, ws *api.Workspace, build *setupBuild) (*api.BaseBase, error) {
	op, err := ws.BuildBase(ctx, build.name, build.body, build.size)
	if err != nil {
		return nil, err
	}
	if op, err = ws.WaitOperation(ctx, op.Id); err != nil {
		return nil, err
	}
	return ws.BaseOf(ctx, op)
}

var teardownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Delete the workspace service, the Zerops project stays",
	Long:  "Deletes the Zerops service of the current workspace with everything in it (sandboxes, bases, snapshots) and forgets it here. The project is left as it is, other services in it are not touched; an empty project created by `zbox setup` can be deleted in the Zerops GUI.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		c, err := loadSelected()
		if err != nil {
			return err
		}
		if c.ServiceId == "" {
			return usagef("nothing to tear down, the current workspace has no Zerops service id (assigned with `zbox workspace use`); `zbox workspace forget` drops it here")
		}
		zc, err := zeropsClient(ctx, &c)
		if err != nil {
			return err
		}
		// Forgetting the workspace clears it from c, the project is named after that.
		project := c.ProjectName
		if err := confirm(fmt.Sprintf("delete the workspace service %s in project %s", currentLabel(c), project), "Every sandbox, base and snapshot in it goes, the project stays."); err != nil {
			return err
		}
		processId, err := zc.DeleteServiceStack(ctx, c.ServiceId)
		if err != nil {
			// Someone already deleted the service elsewhere, so dropping the assignment is the whole job.
			if !zerops.IsNotFound(err) {
				return err
			}
			if err := c.ForgetWorkspace(); err != nil {
				return err
			}
			say("The workspace service was already gone, workspace forgotten.")
			printOthers(c)
			return nil
		}
		if err := zc.WaitProcesses(ctx, []string{processId}, printProcess); err != nil {
			return err
		}
		if err := c.ForgetWorkspace(); err != nil {
			return err
		}
		say("Workspace service deleted, project %s stays.", project)
		printOthers(c)
		return nil
	},
}

// currentLabel names the current workspace for messages: the service id, with the workspace name when it has one. A setup that stopped before the URL was known has only the ids.
func currentLabel(c config.Config) string {
	if w, ok := c.Current(); ok && w.Name != "" {
		return fmt.Sprintf("%s (%s)", w.Name, c.ServiceId)
	}
	return c.ServiceId
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the login, the workspace and whether it answers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		// The one place the beta is said at run time, a hint, not on every command.
		note("%s.", betaNotice)
		c, err := loadSelected()
		if err != nil {
			return err
		}
		st := statusInfo{Api: c.ApiUrl}
		pairs := []string{"API", c.ApiUrl}
		zc, err := zeropsClient(ctx, &c)
		if err != nil {
			pairs = append(pairs, "Login", err.Error())
		} else {
			user, err := zc.UserInfo(ctx)
			if err != nil {
				return err
			}
			st.Login = user.Email
			pairs = append(pairs, "Login", fmt.Sprintf("%s (%s)", user.Email, user.Account))
			if c.HasZeropsWorkspace() {
				project, err := zc.Project(ctx, c.ProjectId)
				if err != nil {
					return forgetGoneWorkspace(&c, err, "Workspace: none, the assigned project no longer exists, run `zbox setup`")
				}
				service, err := zc.ServiceStack(ctx, c.ServiceId)
				if err != nil {
					return forgetGoneWorkspace(&c, err, "Workspace: none, the assigned service no longer exists, run `zbox setup`")
				}
				st.ProjectId, st.ServiceId = c.ProjectId, c.ServiceId
				pairs = append(pairs,
					"Project", fmt.Sprintf("%s (%s) %s", project.Name.Native(), c.ProjectId, project.Status.Native()),
					"Service", fmt.Sprintf("%s (%s) %s", service.Name.Native(), c.ServiceId, service.Status.Native()))
			}
		}
		if !c.HasWorkspace() {
			pairs = append(pairs, "Workspace", "none, `zbox setup` creates one")
			return emit(st, func() { fields(pairs...) })
		}
		w := c.EffectiveWorkspace()
		// A workspace of the account is reached through zproxy, its own API host is only named when it has one.
		label := w.WorkspaceUrl
		if !w.Direct() {
			label = cmp.Or(w.WorkspaceUrl, "service "+w.ServiceId) + ", through zproxy"
		}
		st.Workspace = label
		if ref, source := workspaceRef(); c.Pinned() {
			pairs = append(pairs, "Workspace", fmt.Sprintf("%s (%s %s, the current one is left as it is)", label, source, ref))
		} else {
			pairs = append(pairs, "Workspace", label)
		}
		ws, err := connect(ctx, &c)
		if err != nil {
			st.Error = err.Error()
			pairs = append(pairs, "Orchestrator", fmt.Sprintf("unreachable (%v)", err))
			return emit(st, func() { fields(pairs...) })
		}
		info, err := ws.Info(ctx)
		if err != nil {
			st.Error = err.Error()
			pairs = append(pairs, "Orchestrator", fmt.Sprintf("unreachable (%v)", err))
			return emit(st, func() { fields(pairs...) })
		}
		st.Orchestrator = info
		pairs = append(pairs, "Orchestrator", fmt.Sprintf("%s on %s", info.Version, lower(info.Substrate)), "Domains", domainsString(info.Domains))
		// An orchestrator from before the capacity fields answers zeros, the lines mean nothing then.
		if info.Capacity.Cpus > 0 {
			pairs = append(pairs, "Capacity", capacityString(info.Capacity), "In use", usageString(info.Usage))
		}
		return emit(st, func() { fields(pairs...) })
	},
}

// statusInfo is `zbox workspace status --json`: what the CLI knows here and what the workspace answered.
type statusInfo struct {
	Api          string                 `json:"api"`
	Login        string                 `json:"login,omitempty"`
	ProjectId    string                 `json:"projectId,omitempty"`
	ServiceId    string                 `json:"serviceId,omitempty"`
	Workspace    string                 `json:"workspace,omitempty"`
	Orchestrator *api.ResponseWorkspace `json:"orchestrator,omitempty"`
	Error        string                 `json:"error,omitempty"`
}

// ensureWorkspace offers the setup instead of failing, because `zbox new` is the first command most people reach for and the workspace is a prerequisite they have no reason to know about yet.
func ensureWorkspace(ctx context.Context) (*api.Workspace, error) {
	c, err := loadSelected()
	if err != nil {
		return nil, err
	}
	if !c.HasWorkspace() {
		if !stdinTerminal {
			return nil, notAuthorized{noWorkspaceHint}
		}
		if err := ask(fmt.Sprintf("There is no workspace yet. Create the workspace %s in a new Zerops project %s?", flagSetupName, flagSetupProject)); err != nil {
			return nil, err
		}
		if err := createWorkspace(ctx, &c); err != nil {
			return nil, err
		}
	}
	return connect(ctx, &c)
}

// forgetGoneWorkspace turns a not-found answer into an unassignment, because a workspace whose project was deleted elsewhere is state the CLI should drop rather than report as a failure.
func forgetGoneWorkspace(c *config.Config, err error, message string) error {
	if !zerops.IsNotFound(err) {
		return err
	}
	if err := c.ForgetWorkspace(); err != nil {
		return err
	}
	say("%s", message)
	return nil
}

// printOthers names the remaining workspaces after the current one is gone, so the reader knows a switch is one command away.
func printOthers(c config.Config) {
	// A workspace named for one run was dropped and the current one is still in use, there is nothing to switch to.
	if ref, _ := workspaceRef(); ref != "" {
		if w, ok := c.Current(); ok {
			note("The current workspace stays %s (%s).", w.Name, w.Host())
			return
		}
	}
	if others := c.Others(); len(others) > 0 {
		note("Other workspaces: %s. Switch with `zbox workspace switch <name>`.", strings.Join(others, ", "))
	}
}

func printProcess(p zerops.ProcessState) {
	line := fmt.Sprintf("  %-28s %s", p.Action, p.Status)
	if p.Error != "" {
		line += " " + p.Error
	}
	fmt.Fprintln(os.Stderr, line)
}

// keyReadable says whether an env value read with the token is the key itself: not empty, not the generator placeholder of a secret env, not the REDACTED a read-only role gets for a sensitive env.
func keyReadable(v string) bool {
	return v != "" && v != "REDACTED" && !strings.HasPrefix(v, "<@")
}

// waitForPing holds until the orchestrator answers and accepts the credential.
func waitForPing(ctx context.Context, ws *api.Workspace) error {
	deadline := time.Now().Add(setupPingTimeout)
	for {
		_, err := ws.Info(ctx)
		if err == nil {
			return nil
		}
		if api.IsCode(err, "unauthorized") {
			return fmt.Errorf("the orchestrator rejected the credential, check the %s env of the service", apiKeyEnv)
		}
		if time.Now().After(deadline) {
			return errors.New("the orchestrator did not come up in time, check the service log in the Zerops GUI")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// No template by default since 2026-10-09: a workspace boots from its base, a warm template is asked for with --template.
var defaultTemplates []string

// setupTemplate is one warm template the setup creates through the API once the base is READY. Zero MemoryBytes or Cpus leave the sandbox default. Warmup is the shell command run in the build guest before its memory is captured, empty for none.
type setupTemplate struct {
	Name        string
	MemoryBytes int64
	Cpus        int
	Warmup      string
	// Base is the build or pull the template stands on.
	Base string
}

// setupTemplates reads the --template values, name=cpus:memory@base with every part given, and the --warmup values, name=command, onto the
// template of that name.
func setupTemplates(specs []string, warmups []string) ([]setupTemplate, error) {
	var out []setupTemplate
	for _, spec := range specs {
		name, rest, hasName := strings.Cut(spec, "=")
		size, base, hasBase := strings.Cut(rest, "@")
		cpus, memory, hasMemory := strings.Cut(size, ":")
		if !hasName || !hasBase || !hasMemory || name == "" || base == "" || cpus == "" || memory == "" {
			return nil, fmt.Errorf("--template %q: use name=cpus:memory@base, for example dev=2:4g@hello", spec)
		}
		t := setupTemplate{Name: name, Base: base}
		n, err := strconv.Atoi(cpus)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("--template %q: %q is not a cpu count", spec, cpus)
		}
		t.Cpus = n
		if t.MemoryBytes, err = parseBytes(memory); err != nil {
			return nil, fmt.Errorf("--template %q: %w", spec, err)
		}
		if slices.ContainsFunc(out, func(o setupTemplate) bool { return o.Name == name }) {
			return nil, fmt.Errorf("--template %q: a template is already named %s", spec, name)
		}
		out = append(out, t)
	}
	for _, spec := range warmups {
		name, command, ok := strings.Cut(spec, "=")
		if !ok || name == "" || command == "" {
			return nil, fmt.Errorf("--warmup %q: use name=command, for example dev='claude --version'", spec)
		}
		i := slices.IndexFunc(out, func(t setupTemplate) bool { return t.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("--warmup %q: no --template named %q", spec, name)
		}
		if out[i].Warmup != "" {
			return nil, fmt.Errorf("--warmup %q: template %q already has one", spec, name)
		}
		out[i].Warmup = command
	}
	return out, nil
}

// createSetupTemplate creates one warm template from the base and waits until it is READY, failing with the orchestrator's reason when the build does.
func createSetupTemplate(ctx context.Context, ws *api.Workspace, baseDigest string, t setupTemplate) error {
	warm := true
	req := api.RequestPostTemplate{BaseDigest: baseDigest, Name: optional(t.Name), Warm: &warm, Warmup: optional(t.Warmup), Cpus: optionalInt(t.Cpus)}
	if t.MemoryBytes != 0 {
		req.MemoryBytes = &t.MemoryBytes
	}
	created, err := ws.CreateTemplate(ctx, req)
	if err != nil {
		return fmt.Errorf("template %s: %w", t.Name, err)
	}
	if _, err := waitTemplate(ctx, ws, created.Digest); err != nil {
		return fmt.Errorf("template %s: %w", t.Name, err)
	}
	return nil
}

func init() {
	setupCmd.Flags().StringVar(&flagSetupName, "name", defaultHostname, "hostname of the workspace service, and the name it is known by here")
	setupCmd.Flags().StringVar(&flagSetupProject, "project", "zbox-"+time.Now().Format("20060102150405"), "name of the Zerops project to create, zbox-<timestamp> by default, ignored when --project-id names an existing one")
	setupCmd.Flags().StringVar(&flagSetupProjectId, "project-id", "", "create the workspace in this existing project instead of creating a new one")
	setupCmd.Flags().StringArrayVar(&flagSetupBuilds, "build", nil, "Dockerfile context built into a base on the new workspace, as [name=]<directory or .tar, .tar.gz, .zip>; repeatable, named after the directory or archive by default")
	setupCmd.Flags().BoolVarP(&flagBuildVerbose, "verbose", "v", false, "list every member each --build packs and every file or tree it leaves out")
	setupCmd.Flags().StringArrayVar(&flagSetupBases, "base", nil, "image pulled as a base, as [name=]<image ref or tarball URL>; repeatable, the default sandbox image when neither --base nor --build is given")
	setupCmd.Flags().StringArrayVar(&flagSetupTemplates, "template", defaultTemplates, "warm template built once the bases are in, as name=cpus:memory@base with the base a --build or --base name, for example dev=2:4g@hello; repeatable, none by default, each adds up to a minute to the setup")
	setupCmd.Flags().StringArrayVar(&flagSetupWarmups, "warmup", nil, "shell command a template runs in its guest before the memory is captured, as name=command (for example dev='claude --version'), so what it loads is resident in every spawn; repeatable, one per template name")
	setupCmd.Flags().IntVar(&flagSetupCpu, "cpu", 0, "dedicated cpus of the workspace VM (min 2), the platform's default of 2 when left out")
	setupCmd.Flags().IntVar(&flagSetupRam, "ram", 0, "RAM of the workspace VM in GiB (min 2), the sandboxes take theirs from it; the platform's default of 4 GiB when left out")
	setupCmd.Flags().BoolVar(&flagSetupNoSubdom, "no-subdomain", false, "no subdomain access for the service: the API is reached through zproxy anyway, but the sandbox ports get no public URL, so no zbox open and no public or members ports")
	setupCmd.Flags().IntVar(&flagSetupDisk, "disk", 0, "disk of the workspace VM in GiB (min 10), one pool for bases, sandboxes and snapshots; the platform's default of 10 GiB when left out")
	destructive(teardownCmd)
	rootCmd.AddCommand(setupCmd)
}
