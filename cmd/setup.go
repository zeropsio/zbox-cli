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

	"github.com/zeropsio/zbox-cli/internal/config"
	"github.com/zeropsio/zbox-cli/internal/waiting"
	"github.com/zeropsio/zbox-cli/internal/zboxapi"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

const defaultPoolHostname = "zbox"

// Percentages the stages of the setup are worth. They are rough weights of a wait dominated by the build and the image, and they only ever pull the estimate forward.
const (
	stepImport    = 3
	stepProcesses = 12
	stepUrl       = 60
	stepPing      = 70
	stepImage     = 85
)

var (
	flagSetupName      string
	flagSetupProjectId string
	flagSetupEstimate  time.Duration
	flagTeardownYes    bool
	setupPingTimeout   = 5 * time.Minute
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Create a sandbox pool, in a new Zerops project or in an existing one",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		if c.HasSandboxPool() {
			return fmt.Errorf("a sandbox pool already exists (project %s), run `zbox teardown` first", c.ProjectName)
		}
		return createPool(cmd.Context(), &c)
	},
}

// createPool imports the pool, waits for it to answer and records it in the config. It is shared with `zbox new`, which offers the setup when nothing is assigned yet.
func createPool(ctx context.Context, c *config.Config) error {
	api, err := zeropsClient(*c)
	if err != nil {
		return err
	}
	// Everything from here on goes through the screen, a direct print would land in the middle of the frame it redraws.
	screen := waiting.New(flagSetupEstimate)
	defer screen.Stop()

	var res zerops.ImportResult
	if flagSetupProjectId != "" {
		// The service-stack import carries only the services section, the project is the one named by the flag.
		yaml, err := zerops.RenderImport(zerops.ImportParams{Hostname: flagSetupName, BinaryUrl: flagBinaryUrl})
		if err != nil {
			return err
		}
		fmt.Printf("Creating the sandbox pool %s in project %s.\n", flagSetupName, flagSetupProjectId)
		screen.Start()
		screen.Step("Creating the sandbox pool", stepImport)
		res, err = api.ImportServiceStack(ctx, flagSetupProjectId, yaml)
		if err != nil {
			return err
		}
	} else {
		user, err := api.UserInfo(ctx)
		if err != nil {
			return err
		}
		yaml, err := zerops.RenderImport(zerops.ImportParams{ProjectName: flagSetupName, Hostname: defaultPoolHostname, BinaryUrl: flagBinaryUrl})
		if err != nil {
			return err
		}
		fmt.Printf("Creating project %s in %s.\n", flagSetupName, user.Account)
		screen.Start()
		screen.Step("Creating the project", stepImport)
		res, err = api.ImportProject(ctx, user.ClientId, yaml)
		if err != nil {
			return err
		}
		c.ClientId = user.ClientId
	}
	c.ProjectId = res.ProjectId
	c.ProjectName = res.ProjectName
	c.ServiceId = res.ServiceId
	if err := c.Save(); err != nil {
		return err
	}
	screen.Step("Building and starting the sandbox pool", stepProcesses)
	// The Zerops processes behind the import are not shown, their names describe the plumbing rather than anything the reader can act on, and a failure is reported as an error anyway.
	if err := api.WaitProcesses(ctx, res.Processes, func(zerops.ProcessState) {}); err != nil {
		return err
	}
	screen.Step("Reading the public URL of the pool", stepUrl)
	subdomain, err := waitForEnv(ctx, api, res.ServiceId, "zeropsSubdomain")
	if err != nil {
		return err
	}
	c.ZboxUrl = strings.TrimSpace(strings.Split(subdomain, "\n")[0])
	if err := c.Save(); err != nil {
		return err
	}
	screen.Step("Waiting for the orchestrator to answer", stepPing)
	if err := waitForPing(ctx, zboxapi.New(c.ZboxUrl), screen); err != nil {
		return err
	}
	screen.Stop()
	fmt.Printf("The sandbox pool is ready: %s\nCreate your first sandbox with `zbox new`.\n", c.ZboxUrl)
	return nil
}

var teardownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Delete the Zerops project that hosts the sandbox pool",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		c, err := loadConfig()
		if err != nil {
			return err
		}
		if c.ProjectId == "" {
			return errors.New("nothing to tear down")
		}
		api, err := zeropsClient(c)
		if err != nil {
			return err
		}
		if !flagTeardownYes {
			fmt.Printf("Delete project %s (%s) and the whole sandbox pool in it? [y/N] ", c.ProjectName, c.ProjectId)
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				return errors.New("aborted")
			}
		}
		processId, err := api.DeleteProject(ctx, c.ProjectId)
		if err != nil {
			// Someone already deleted the project elsewhere, so dropping the assignment is the whole job.
			if !zerops.IsNotFound(err) {
				return err
			}
			if err := c.ForgetSandboxPool(); err != nil {
				return err
			}
			fmt.Println("Project was already gone, sandbox pool unassigned.")
			return nil
		}
		if err := api.WaitProcesses(ctx, []string{processId}, printProcess); err != nil {
			return err
		}
		if err := c.ForgetSandboxPool(); err != nil {
			return err
		}
		fmt.Println("Project deleted.")
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the login, the sandbox pool and its readiness",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		c, err := loadConfig()
		if err != nil {
			return err
		}
		fmt.Printf("API:      %s\n", c.ApiUrl)
		api, err := zeropsClient(c)
		if err != nil {
			fmt.Println("Login:    not logged in")
			return nil
		}
		user, err := api.UserInfo(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Login:    %s (%s)\n", user.Email, user.Account)
		if !c.HasSandboxPool() {
			fmt.Println("Pool:     none, run `zbox setup`")
			return nil
		}
		project, err := api.Project(ctx, c.ProjectId)
		if err != nil {
			return forgetGonePool(&c, err, "Pool:     none, the assigned project no longer exists, run `zbox setup`")
		}
		pool, err := api.ServiceStack(ctx, c.ServiceId)
		if err != nil {
			return forgetGonePool(&c, err, "Pool:     none, the assigned service no longer exists, run `zbox setup`")
		}
		fmt.Printf("Project:  %s (%s) %s\n", project.Name.Native(), c.ProjectId, project.Status.Native())
		fmt.Printf("Pool:     %s (%s) %s\n", pool.Name.Native(), c.ServiceId, pool.Status.Native())
		fmt.Printf("URL:      %s\n", c.ZboxUrl)
		ping, err := zboxapi.New(c.ZboxUrl).Ping(ctx)
		if err != nil {
			fmt.Printf("Orchestrator: unreachable (%v)\n", err)
			return nil
		}
		fmt.Printf("Orchestrator: ok, default image ready: %v\n", ping.ImageReady)
		return nil
	},
}

// ensurePool offers the setup instead of failing, because `zbox new` is the first command most people reach for and the pool is a prerequisite they have no reason to know about yet.
func ensurePool(ctx context.Context, c *config.Config) error {
	if c.HasSandboxPool() {
		return nil
	}
	fmt.Printf("No sandbox pool yet. Create one now (new Zerops project %s)? [y/N] ", flagSetupName)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return errors.New("aborted")
	}
	return createPool(ctx, c)
}

// forgetGonePool turns a not-found answer into an unassignment, because a pool whose project was deleted elsewhere is state the CLI should drop rather than report as a failure.
func forgetGonePool(c *config.Config, err error, message string) error {
	if !zerops.IsNotFound(err) {
		return err
	}
	if err := c.ForgetSandboxPool(); err != nil {
		return err
	}
	fmt.Println(message)
	return nil
}

func printProcess(p zerops.ProcessState) {
	line := fmt.Sprintf("  %-28s %s", p.Action, p.Status)
	if p.Error != "" {
		line += " " + p.Error
	}
	fmt.Println(line)
}

func waitForEnv(ctx context.Context, api *zerops.Client, serviceId, key string) (string, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		value, found, err := api.ServiceEnv(ctx, serviceId, key)
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

// waitForPing holds until the pool can actually serve a `zbox new`, which means the orchestrator answers and its default sandbox image is in place.
// Reporting the pool as ready any earlier only moves the wait to the first create, where it surfaces as an error instead of as progress.
func waitForPing(ctx context.Context, client *zboxapi.Client, screen *waiting.Screen) error {
	deadline := time.Now().Add(setupPingTimeout)
	announced := false
	for {
		ping, err := client.Ping(ctx)
		if err == nil {
			if ping.ImageReady {
				return nil
			}
			if !announced {
				screen.Step("Baking the default sandbox image into the pool", stepImage)
				announced = true
			}
		}
		if time.Now().After(deadline) {
			if announced {
				return errors.New("the default sandbox image did not get ready in time, check the service log in the Zerops GUI")
			}
			return errors.New("the orchestrator did not come up in time, check the service log in the Zerops GUI")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func init() {
	setupCmd.Flags().StringVar(&flagSetupName, "name", defaultPoolHostname, "name of the Zerops project to create, or hostname of the pool service when --project-id is given")
	setupCmd.Flags().StringVar(&flagSetupProjectId, "project-id", "", "create the pool in this existing project instead of creating a new one")
	setupCmd.Flags().DurationVar(&flagSetupEstimate, "estimate", 3*time.Minute, "how long the setup is expected to take, the progress estimate is measured against it")
	teardownCmd.Flags().BoolVarP(&flagTeardownYes, "yes", "y", false, "skip the confirmation")
	rootCmd.AddCommand(setupCmd, teardownCmd, statusCmd)
}
