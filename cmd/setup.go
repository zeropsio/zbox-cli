package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
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
	// defaultBaseImage is the image every workspace gets as its first base, pulled by the orchestrator from the tarball URL.
	defaultBaseImage = config.DefaultBaseImage
	// The service env the Sandbox Workspace controller generates the workspace API key into at service create.
	apiKeyEnv = "ZBOX_WORKSPACE_API_KEY"
	// How long the key env gets to show up before the GUI hand-over is used instead; the subdomain env is already there by then.
	keyEnvTimeout = 30 * time.Second
)

var (
	flagSetupName      string
	flagSetupProjectId string
	flagSetupImage     string
	flagTeardownYes    bool
	setupPingTimeout   = 5 * time.Minute
	setupBaseTimeout   = 15 * time.Minute
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Create a workspace, in a new Zerops project or in an existing one",
	RunE: func(cmd *cobra.Command, args []string) error {
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

// createWorkspace imports the service, waits for it to answer, stores its URL and key and gives it its first base. It is shared with `zbox new`, which offers the setup when nothing is assigned yet.
func createWorkspace(ctx context.Context, c *config.Config) error {
	zc, err := zeropsClient(*c)
	if err != nil {
		return err
	}
	// The screen prints the stages and, on a TTY, a note about workspaces every few seconds, so the minutes of the build do not look like a hang.
	screen := waiting.New()
	defer screen.Stop()

	var res zerops.ImportResult
	if flagSetupProjectId != "" {
		// The service-stack import carries only the services section, the project is the one named by the flag.
		yaml, err := zerops.RenderImport(zerops.ImportParams{Hostname: flagSetupName})
		if err != nil {
			return err
		}
		fmt.Printf("Creating the workspace %s in project %s.\n", flagSetupName, flagSetupProjectId)
		screen.Start()
		screen.Step("Creating the workspace")
		res, err = zc.ImportServiceStack(ctx, flagSetupProjectId, yaml)
		if err != nil {
			return err
		}
	} else {
		user, err := zc.UserInfo(ctx)
		if err != nil {
			return err
		}
		yaml, err := zerops.RenderImport(zerops.ImportParams{ProjectName: flagSetupName, Hostname: defaultHostname})
		if err != nil {
			return err
		}
		fmt.Printf("Creating project %s in %s.\n", flagSetupName, user.Account)
		screen.Start()
		screen.Step("Creating the project")
		res, err = zc.ImportProject(ctx, user.ClientId, yaml)
		if err != nil {
			return err
		}
		c.ClientId = user.ClientId
	}
	// The ids are stored before the URL is known, so a setup that fails from here on can still be torn down. A `zbox switch` in between loses that partial state, the list only keeps workspaces with a URL.
	c.ProjectId = res.ProjectId
	c.ProjectName = res.ProjectName
	c.ServiceId = res.ServiceId
	c.WorkspaceUrl, c.ApiKey = "", ""
	if err := c.Save(); err != nil {
		return err
	}
	screen.Step("Building and starting the workspace")
	// The Zerops processes behind the import are not shown, their names describe the plumbing rather than anything the reader can act on, and a failure is reported as an error anyway.
	if err := zc.WaitProcesses(ctx, res.Processes, func(zerops.ProcessState) {}); err != nil {
		return err
	}
	screen.Step("Reading the URL and the API key of the workspace")
	// The service publishes the API host first and the ingress host second. Only the API host is stored, the other comes from GET /workspace.
	subdomain, err := waitForEnv(ctx, zc, res.ServiceId, "zeropsSubdomain", envTimeout)
	if err != nil {
		return err
	}
	workspaceUrl := strings.TrimSpace(strings.Split(subdomain, "\n")[0])
	// The key is a sensitive generated env. A personal access token is always in sudo mode, so the env list gives the value in clear and it is stored as read; a read-only project role gets the literal REDACTED instead, and a row that never appears or a generator placeholder means the same: the Zerops GUI reveals it for the user and hands it over on the loopback.
	apiKey, err := waitForEnv(ctx, zc, res.ServiceId, apiKeyEnv, keyEnvTimeout)
	if err != nil && !errors.Is(err, errEnvMissing) {
		return err
	}
	if !keyReadable(apiKey) {
		screen.Pause()
		fmt.Printf("The workspace API key (%s) is a generated secret, the Zerops GUI hands it over. Confirm there.\n", apiKeyEnv)
		w, err := authorizeWorkspace(ctx, c, zc, res.ServiceId, flagSetupName, func(s string) { fmt.Println(s) })
		if err != nil {
			return err
		}
		if err := c.Assign(w); err != nil {
			return err
		}
		screen.Resume()
	} else if err := c.Assign(config.Workspace{Name: flagSetupName, ProjectId: res.ProjectId, ProjectName: res.ProjectName, ServiceId: res.ServiceId, WorkspaceUrl: workspaceUrl, ApiKey: apiKey}); err != nil {
		return err
	}
	screen.Step("Waiting for the orchestrator to answer")
	ws := api.New(c.WorkspaceUrl, c.ApiKey)
	if err := waitForPing(ctx, ws); err != nil {
		return err
	}
	screen.Step("Adding the default base image")
	base, err := ensureBase(ctx, ws, flagSetupImage)
	if err != nil {
		return err
	}
	screen.Stop()
	fmt.Printf("The workspace is ready: %s\nDefault base %s (%s).\nCreate your first sandbox with `zbox new`, open the console with `zbox open`, or find the workspace in the Zerops GUI under zbox.\n", c.WorkspaceUrl, short(base.Digest), base.ImageRef)
	return nil
}

var teardownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Delete the workspace service, the Zerops project stays",
	Long:  "Deletes the Zerops service of the current workspace with everything in it (sandboxes, bases, snapshots) and forgets it here. The project is left as it is, other services in it are not touched; an empty project created by `zbox setup` can be deleted in the Zerops GUI.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		c, err := loadConfig()
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
		if !flagTeardownYes {
			fmt.Printf("Delete the workspace service %s in project %s with every sandbox in it? The project stays. [y/N] ", currentLabel(c), c.ProjectName)
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
		fmt.Printf("Workspace service deleted, project %s stays.\n", c.ProjectName)
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
		c, err := loadConfig()
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
		fmt.Printf("Workspace: %s\n", url)
		ws := api.New(url, key)
		info, err := ws.Info(ctx)
		if err != nil {
			fmt.Printf("Orchestrator: unreachable (%v)\n", err)
			return nil
		}
		fmt.Printf("Orchestrator: %s on %s, ingress %s\n", info.Version, strings.ToLower(string(info.Substrate)), info.IngressHost)
		return nil
	},
}

// ensureWorkspace offers the setup instead of failing, because `zbox new` is the first command most people reach for and the workspace is a prerequisite they have no reason to know about yet.
func ensureWorkspace(ctx context.Context) (*api.Workspace, error) {
	c, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if !c.HasWorkspace() {
		fmt.Printf("No workspace yet. Create one now (new Zerops project %s)? [y/N] ", flagSetupName)
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

// ensureBase registers the image as a base unless one already exists for it, and waits until it is READY. A workspace with no base cannot serve `zbox new`.
func ensureBase(ctx context.Context, ws *api.Workspace, imageRef string) (*api.BaseBase, error) {
	bases, err := ws.Bases(ctx)
	if err != nil {
		return nil, err
	}
	var base *api.BaseBase
	for i := range bases {
		if bases[i].ImageRef == imageRef && bases[i].Status != api.BaseBaseStatusFAILED {
			base = &bases[i]
		}
	}
	if base == nil {
		base, err = ws.CreateBase(ctx, api.RequestPostBase{ImageRef: imageRef})
		if err != nil {
			return nil, err
		}
	}
	return waitForBase(ctx, ws, base.Digest, setupBaseTimeout)
}

func waitForBase(ctx context.Context, ws *api.Workspace, digest string, timeout time.Duration) (*api.BaseBase, error) {
	deadline := time.Now().Add(timeout)
	for {
		base, err := ws.Base(ctx, digest)
		if err != nil {
			return nil, err
		}
		switch base.Status {
		case api.BaseBaseStatusREADY:
			return base, nil
		case api.BaseBaseStatusFAILED:
			return base, fmt.Errorf("base %s failed: %s", short(digest), dash(str(base.FailedReason)))
		}
		if time.Now().After(deadline) {
			return base, fmt.Errorf("base %s did not get ready in time (%s)", short(digest), base.Status)
		}
		select {
		case <-ctx.Done():
			return base, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func init() {
	setupCmd.Flags().StringVar(&flagSetupName, "name", defaultHostname, "name of the Zerops project to create, or hostname of the workspace service when --project-id is given")
	setupCmd.Flags().StringVar(&flagSetupProjectId, "project-id", "", "create the workspace in this existing project instead of creating a new one")
	setupCmd.Flags().StringVar(&flagSetupImage, "image", defaultBaseImage, "image registered as the first base of the workspace")
	teardownCmd.Flags().BoolVarP(&flagTeardownYes, "yes", "y", false, "skip the confirmation")
	rootCmd.AddCommand(setupCmd, teardownCmd, statusCmd)
}
