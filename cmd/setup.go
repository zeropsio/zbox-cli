package cmd

import (
	"bufio"
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
	// The service env the Sandbox Workspace controller generates the workspace API key into at service create.
	apiKeyEnv = "ZBOX_WORKSPACE_API_KEY"
	// How long the key env gets to show up before the GUI hand-over is used instead; the subdomain env is already there by then.
	keyEnvTimeout = 30 * time.Second
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
	flagTeardownYes    bool
	setupPingTimeout   = 5 * time.Minute
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Create a workspace, in a new Zerops project or in an existing one",
	Long: `Creates the workspace service --name (in a new project --project, or in the one --project-id names), waits for it and stores its URL and key
here. Then, in this order: every --build is uploaded and built into a base, every --base is pulled, and every --template is warmed on the base
it names. Without --build and --base the default sandbox image is pulled.`,
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
			return fmt.Errorf("a workspace named %s is already known (%s), pick another --name or run `zbox teardown` or `zbox workspace forget` on it first", w.Name, w.Host())
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

// createWorkspace brings the workspace up, stores its URL and key and runs the chain of builds, pulls and templates. It is shared with `zbox new`, which offers the setup when nothing is assigned yet.
func createWorkspace(ctx context.Context, c *config.Config) error {
	zc, err := zeropsClient(*c)
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
			printSetupDone(c.WorkspaceUrl, bases, nil)
			return fmt.Errorf("%w; the workspace is up, `zbox template create --warm` builds the template again", err)
		}
	}
	screen.Stop()
	printSetupDone(c.WorkspaceUrl, bases, templates)
	return nil
}

func printSetupDone(url string, bases []*api.BaseBase, templates []setupTemplate) {
	fmt.Printf("The workspace is ready: %s\n", url)
	for _, b := range bases {
		fmt.Printf("Base %s (%s).\n", short(b.Digest), baseLabel(b))
	}
	if len(templates) > 0 {
		names := make([]string, len(templates))
		for i, t := range templates {
			names[i] = t.Name
		}
		fmt.Printf("Warm templates: %s.\n", strings.Join(names, ", "))
	}
	fmt.Println("Create your first sandbox with `zbox new`, open the console with `zbox console`, or find the workspace in the Zerops GUI.")
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

// newWorkspace imports the service, waits for it to answer and stores its URL and key. The import names no base, the chain registers them through the API.
func newWorkspace(ctx context.Context, c *config.Config, zc *zerops.Client, screen *waiting.Screen) (*api.Workspace, error) {
	var res zerops.ImportResult
	if flagSetupProjectId != "" {
		// The service-stack import carries only the services section, the project is the one named by the flag.
		yaml, err := zerops.RenderImport(zerops.ImportParams{Hostname: flagSetupName, Cpu: flagSetupCpu, Ram: flagSetupRam, Disk: flagSetupDisk})
		if err != nil {
			return nil, err
		}
		fmt.Printf("Creating the workspace %s in project %s.\n", flagSetupName, flagSetupProjectId)
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
		yaml, err := zerops.RenderImport(zerops.ImportParams{ProjectName: flagSetupProject, Hostname: flagSetupName, Cpu: flagSetupCpu, Ram: flagSetupRam, Disk: flagSetupDisk})
		if err != nil {
			return nil, err
		}
		fmt.Printf("Creating project %s with the workspace %s in %s.\n", flagSetupProject, flagSetupName, user.Account)
		screen.Start()
		screen.Step("Creating the project")
		res, err = zc.ImportProject(ctx, user.ClientId, yaml)
		if err != nil {
			return nil, err
		}
		c.ClientId = user.ClientId
	}
	// The ids are stored before the URL is known, so a setup that fails from here on can still be torn down. A `zbox switch` in between loses that partial state, the list only keeps workspaces with a URL.
	c.ProjectId = res.ProjectId
	c.ProjectName = res.ProjectName
	c.ServiceId = res.ServiceId
	c.WorkspaceUrl, c.ApiKey = "", ""
	if err := c.Save(); err != nil {
		return nil, err
	}
	screen.Step("Starting the workspace")
	// The Zerops processes behind the import are not shown, their names describe the plumbing rather than anything the reader can act on, and a failure is reported as an error anyway.
	if err := zc.WaitProcesses(ctx, res.Processes, func(zerops.ProcessState) {}); err != nil {
		return nil, err
	}
	screen.Step("Reading the URL and the API key of the workspace")
	// The service publishes the API host first and the ingress host second. Only the API host is stored, the other comes from GET /workspace.
	subdomain, err := waitForEnv(ctx, zc, res.ServiceId, "zeropsSubdomain", envTimeout)
	if err != nil {
		return nil, err
	}
	workspaceUrl := strings.TrimSpace(strings.Split(subdomain, "\n")[0])
	// The key is a sensitive generated env. A personal access token is always in sudo mode, so the env list gives the value in clear and it is stored as read; a read-only project role gets the literal REDACTED instead, and a row that never appears or a generator placeholder means the same: the Zerops GUI reveals it for the user and hands it over on the loopback.
	apiKey, err := waitForEnv(ctx, zc, res.ServiceId, apiKeyEnv, keyEnvTimeout)
	if err != nil && !errors.Is(err, errEnvMissing) {
		return nil, err
	}
	if !keyReadable(apiKey) {
		screen.Pause()
		fmt.Printf("The workspace API key (%s) is a generated secret, the Zerops GUI hands it over. Confirm there.\n", apiKeyEnv)
		w, err := authorizeWorkspace(ctx, c, zc, res.ServiceId, flagSetupName, func(s string) { fmt.Println(s) })
		if err != nil {
			return nil, err
		}
		if err := c.Assign(w); err != nil {
			return nil, err
		}
		screen.Resume()
	} else if err := c.Assign(config.Workspace{Name: flagSetupName, ProjectId: res.ProjectId, ProjectName: res.ProjectName, ServiceId: res.ServiceId, WorkspaceUrl: workspaceUrl, ApiKey: apiKey}); err != nil {
		return nil, err
	}
	screen.Step("Waiting for the orchestrator to answer")
	ws := api.New(c.WorkspaceUrl, c.ApiKey)
	if err := waitForPing(ctx, ws); err != nil {
		return nil, err
	}
	return ws, nil
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
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		c, err := loadSelected()
		if err != nil {
			return err
		}
		if c.ServiceId == "" {
			return errors.New("nothing to tear down, the current workspace has no Zerops service id (assigned with `zbox workspace use`); `zbox workspace forget` drops it here")
		}
		zc, err := zeropsClient(c)
		if err != nil {
			return err
		}
		// Forgetting the workspace clears it from c, the project is named after that.
		project := c.ProjectName
		if !flagTeardownYes {
			fmt.Printf("Delete the workspace service %s in project %s with every sandbox in it? The project stays. [y/N] ", currentLabel(c), project)
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				return errors.New("aborted")
			}
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
			fmt.Println("The workspace service was already gone, workspace forgotten.")
			printOthers(c)
			return nil
		}
		if err := zc.WaitProcesses(ctx, []string{processId}, printProcess); err != nil {
			return err
		}
		if err := c.ForgetWorkspace(); err != nil {
			return err
		}
		fmt.Printf("Workspace service deleted, project %s stays.\n", project)
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
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		c, err := loadSelected()
		if err != nil {
			return err
		}
		fmt.Printf("API:       %s\n", c.ApiUrl)
		zc, err := zeropsClient(c)
		if err != nil {
			fmt.Println("Login:     not logged in")
		} else {
			user, err := zc.UserInfo(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("Login:     %s (%s)\n", user.Email, user.Account)
			if c.HasZeropsWorkspace() {
				project, err := zc.Project(ctx, c.ProjectId)
				if err != nil {
					return forgetGoneWorkspace(&c, err, "Workspace: none, the assigned project no longer exists, run `zbox setup`")
				}
				service, err := zc.ServiceStack(ctx, c.ServiceId)
				if err != nil {
					return forgetGoneWorkspace(&c, err, "Workspace: none, the assigned service no longer exists, run `zbox setup`")
				}
				fmt.Printf("Project:   %s (%s) %s\n", project.Name.Native(), c.ProjectId, project.Status.Native())
				fmt.Printf("Service:   %s (%s) %s\n", service.Name.Native(), c.ServiceId, service.Status.Native())
			}
		}
		if !c.HasWorkspace() {
			fmt.Println("Workspace: none, run `zbox setup`")
			return nil
		}
		url, key := c.EffectiveWorkspace()
		if ref, source := workspaceRef(); c.Pinned() {
			fmt.Printf("Workspace: %s (%s %s, the current one is left as it is)\n", url, source, ref)
		} else {
			fmt.Printf("Workspace: %s\n", url)
		}
		ws := api.New(url, key)
		info, err := ws.Info(ctx)
		if err != nil {
			fmt.Printf("Orchestrator: unreachable (%v)\n", err)
			return nil
		}
		fmt.Printf("Orchestrator: %s on %s, ingress %s\n", info.Version, strings.ToLower(string(info.Substrate)), info.IngressHost)
		// An orchestrator from before the capacity fields answers zeros, the lines mean nothing then.
		if info.Capacity.Cpus > 0 {
			fmt.Printf("Capacity: %s\nIn use: %s\n", capacityString(info.Capacity), usageString(info.Usage))
		}
		return nil
	},
}

// ensureWorkspace offers the setup instead of failing, because `zbox new` is the first command most people reach for and the workspace is a prerequisite they have no reason to know about yet.
func ensureWorkspace(ctx context.Context) (*api.Workspace, error) {
	c, err := loadSelected()
	if err != nil {
		return nil, err
	}
	if !c.HasWorkspace() {
		fmt.Printf("No workspace yet. Create one now (workspace %s in a new Zerops project %s)? [y/N] ", flagSetupName, flagSetupProject)
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			return nil, errors.New("aborted")
		}
		if err := createWorkspace(ctx, &c); err != nil {
			return nil, err
		}
	}
	url, key := c.EffectiveWorkspace()
	return api.New(url, key), nil
}

// forgetGoneWorkspace turns a not-found answer into an unassignment, because a workspace whose project was deleted elsewhere is state the CLI should drop rather than report as a failure.
func forgetGoneWorkspace(c *config.Config, err error, message string) error {
	if !zerops.IsNotFound(err) {
		return err
	}
	if err := c.ForgetWorkspace(); err != nil {
		return err
	}
	fmt.Println(message)
	return nil
}

// printOthers names the remaining workspaces after the current one is gone, so the reader knows a switch is one command away.
func printOthers(c config.Config) {
	// A workspace named for one run was dropped and the current one is still in use, there is nothing to switch to.
	if ref, _ := workspaceRef(); ref != "" {
		if w, ok := c.Current(); ok {
			fmt.Printf("The current workspace stays %s (%s).\n", w.Name, w.Host())
			return
		}
	}
	if others := c.Others(); len(others) > 0 {
		fmt.Printf("Other workspaces: %s. Switch with `zbox switch <name>`.\n", strings.Join(others, ", "))
	}
}

func printProcess(p zerops.ProcessState) {
	line := fmt.Sprintf("  %-28s %s", p.Action, p.Status)
	if p.Error != "" {
		line += " " + p.Error
	}
	fmt.Println(line)
}

// keyReadable says whether an env value read with the token is the key itself: not empty, not the generator placeholder of a secret env, not the REDACTED a read-only role gets for a sensitive env.
func keyReadable(v string) bool {
	return v != "" && v != "REDACTED" && !strings.HasPrefix(v, "<@")
}

// errEnvMissing is the timeout of waitForEnv, so a caller with a fallback can tell it from an API failure.
var errEnvMissing = errors.New("service variable did not appear")

const envTimeout = 2 * time.Minute

func waitForEnv(ctx context.Context, zc *zerops.Client, serviceId, key string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		value, found, err := zc.ServiceEnv(ctx, serviceId, key)
		if err != nil {
			return "", err
		}
		if found && value != "" {
			return value, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%w: %s", errEnvMissing, key)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// waitForPing holds until the orchestrator answers and accepts the key.
func waitForPing(ctx context.Context, ws *api.Workspace) error {
	deadline := time.Now().Add(setupPingTimeout)
	for {
		_, err := ws.Info(ctx)
		if err == nil {
			return nil
		}
		if api.IsCode(err, "unauthorized") {
			return fmt.Errorf("the orchestrator rejected the workspace API key, check the %s env of the service", apiKeyEnv)
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
	setupCmd.Flags().IntVar(&flagSetupDisk, "disk", 0, "disk of the workspace VM in GiB (min 10), one pool for bases, sandboxes and snapshots; the platform's default of 10 GiB when left out")
	teardownCmd.Flags().BoolVarP(&flagTeardownYes, "yes", "y", false, "skip the confirmation")
	rootCmd.AddCommand(setupCmd, teardownCmd, statusCmd)
}
