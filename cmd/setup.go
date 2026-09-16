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
	// DefaultBaseImage is the image every workspace gets as its first base, the same one the import YAML loads into the service with prepare-image.
	defaultBaseImage = "zbox-base:latest"
	// The service env the workspace API key is generated into.
	apiKeyEnv = "apiKey"
)

// Percentages the stages of the setup are worth. They are rough weights of a wait dominated by the build and the base, and they only ever pull the estimate forward.
const (
	stepImport    = 3
	stepProcesses = 12
	stepUrl       = 60
	stepPing      = 70
	stepBase      = 85
)

var (
	flagSetupName      string
	flagSetupProjectId string
	flagSetupEstimate  time.Duration
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
	// Everything from here on goes through the screen, a direct print would land in the middle of the frame it redraws.
	screen := waiting.New(flagSetupEstimate)
	defer screen.Stop()

	var res zerops.ImportResult
	if flagSetupProjectId != "" {
		// The service-stack import carries only the services section, the project is the one named by the flag.
		yaml, err := zerops.RenderImport(zerops.ImportParams{Hostname: flagSetupName, ImageUrl: flagImageUrl})
		if err != nil {
			return err
		}
		fmt.Printf("Creating the workspace %s in project %s.\n", flagSetupName, flagSetupProjectId)
		screen.Start()
		screen.Step("Creating the workspace", stepImport)
		res, err = zc.ImportServiceStack(ctx, flagSetupProjectId, yaml)
		if err != nil {
			return err
		}
	} else {
		user, err := zc.UserInfo(ctx)
		if err != nil {
			return err
		}
		yaml, err := zerops.RenderImport(zerops.ImportParams{ProjectName: flagSetupName, Hostname: defaultHostname, ImageUrl: flagImageUrl})
		if err != nil {
			return err
		}
		fmt.Printf("Creating project %s in %s.\n", flagSetupName, user.Account)
		screen.Start()
		screen.Step("Creating the project", stepImport)
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
	screen.Step("Building and starting the workspace", stepProcesses)
	// The Zerops processes behind the import are not shown, their names describe the plumbing rather than anything the reader can act on, and a failure is reported as an error anyway.
	if err := zc.WaitProcesses(ctx, res.Processes, func(zerops.ProcessState) {}); err != nil {
		return err
	}
	screen.Step("Reading the URL and the API key of the workspace", stepUrl)
	// The service publishes the API host first and the ingress host second. Only the API host is stored, the other comes from GET /workspace.
	subdomain, err := waitForEnv(ctx, zc, res.ServiceId, "zeropsSubdomain")
	if err != nil {
		return err
	}
	workspaceUrl := strings.TrimSpace(strings.Split(subdomain, "\n")[0])
	apiKey, err := waitForEnv(ctx, zc, res.ServiceId, apiKeyEnv)
	if err != nil {
		return err
	}
	// The env list gives a generated secret away only as its generator placeholder, a reveal needs sudo mode, which a token does not have. The Zerops GUI reveals it for the user and hands it over on the loopback.
	if strings.HasPrefix(apiKey, "<@") {
		screen.Pause()
		fmt.Println("The workspace API key is a generated secret, the Zerops GUI hands it over. Confirm there.")
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
	screen.Step("Waiting for the orchestrator to answer", stepPing)
	ws := api.New(c.WorkspaceUrl, c.ApiKey)
	if err := waitForPing(ctx, ws); err != nil {
		return err
	}
	screen.Step("Adding the default base image", stepBase)
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
	Short: "Delete the Zerops project that hosts the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		c, err := loadConfig()
		if err != nil {
			return err
		}
		if c.ProjectId == "" {
			return errors.New("nothing to tear down, the assigned workspace was not created by this CLI")
		}
		zc, err := zeropsClient(c)
		if err != nil {
			return err
		}
		if !flagTeardownYes {
			fmt.Printf("Delete project %s (%s) and the whole workspace in it? [y/N] ", c.ProjectName, c.ProjectId)
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				return errors.New("aborted")
			}
		}
		processId, err := zc.DeleteProject(ctx, c.ProjectId)
		if err != nil {
			// Someone already deleted the project elsewhere, so dropping the assignment is the whole job.
			if !zerops.IsNotFound(err) {
				return err
			}
			if err := c.ForgetWorkspace(); err != nil {
				return err
			}
			fmt.Println("Project was already gone, workspace unassigned.")
			return nil
		}
		if err := zc.WaitProcesses(ctx, []string{processId}, printProcess); err != nil {
			return err
		}
		if err := c.ForgetWorkspace(); err != nil {
			return err
		}
		fmt.Println("Project deleted.")
		printOthers(c)
		return nil
	},
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

func waitForEnv(ctx context.Context, zc *zerops.Client, serviceId, key string) (string, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		value, found, err := zc.ServiceEnv(ctx, serviceId, key)
		if err != nil {
			return "", err
		}
		if found && value != "" {
			return value, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("service variable %s did not appear", key)
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
			return errors.New("the orchestrator rejected the workspace API key, check the apiKey env of the service")
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
	setupCmd.Flags().DurationVar(&flagSetupEstimate, "estimate", 3*time.Minute, "how long the setup is expected to take, the progress estimate is measured against it")
	setupCmd.Flags().StringVar(&flagSetupImage, "image", defaultBaseImage, "image registered as the first base of the workspace")
	teardownCmd.Flags().BoolVarP(&flagTeardownYes, "yes", "y", false, "skip the confirmation")
	rootCmd.AddCommand(setupCmd, teardownCmd, statusCmd)
}
