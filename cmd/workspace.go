package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
	"github.com/zeropsio/zbox-cli/internal/browser"
	"github.com/zeropsio/zbox-cli/internal/config"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

var (
	flagWorkspaceKey  string
	flagWorkspaceName string
	flagPolicyFile    string
)

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Show and configure the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		info, err := ws.Info(cmd.Context())
		if err != nil {
			return err
		}
		return emit(info, func() {
			fields(
				"API", ws.BaseUrl,
				"API host", info.ApiHost,
				"Ingress host", info.IngressHost,
				"Version", info.Version,
				"Substrate", strings.ToLower(string(info.Substrate)),
				"Always denied", dash(strings.Join(info.AlwaysDenied, ", ")),
			)
			fmt.Println("Policy:")
			_ = printJson(info.Policy)
		})
	},
}

var workspacePolicyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Print the workspace policy, or replace it with -f",
	Long:  "Without -f the policy is printed as JSON. With -f the JSON file (or - for stdin) replaces the whole policy, so start from the printed one.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		if flagPolicyFile == "" {
			info, err := ws.Info(cmd.Context())
			if err != nil {
				return err
			}
			return printJson(info.Policy)
		}
		var body api.RequestPutWorkspacePolicy
		if err := readJson(flagPolicyFile, &body); err != nil {
			return err
		}
		info, err := ws.PutWorkspacePolicy(cmd.Context(), body)
		if err != nil {
			return err
		}
		return emit(info, func() { fmt.Println("Workspace policy updated.") })
	},
}

var workspaceUseCmd = &cobra.Command{
	Use:   "use <api-url>",
	Short: "Point the CLI at an existing workspace by its API URL and key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagWorkspaceKey == "" {
			return errors.New("--api-key is required, it is the ZBOX_WORKSPACE_API_KEY env of the workspace service")
		}
		c, err := loadConfig()
		if err != nil {
			return err
		}
		url := strings.TrimSuffix(args[0], "/")
		if !strings.Contains(url, "://") {
			url = "https://" + url
		}
		info, err := api.New(url, flagWorkspaceKey).Info(cmd.Context())
		if err != nil {
			return fmt.Errorf("the workspace did not answer: %w", err)
		}
		// A workspace pointed at by hand has no Zerops ids the CLI could tear down.
		w := config.Workspace{Name: flagWorkspaceName, WorkspaceUrl: url, ApiKey: flagWorkspaceKey}
		if err := c.Assign(w); err != nil {
			return err
		}
		fmt.Printf("Using workspace %s: %s (%s on %s).\n", cmp.Or(w.Name, config.DefaultName(url)), url, info.Version, strings.ToLower(string(info.Substrate)))
		return nil
	},
}

var workspaceForgetCmd = &cobra.Command{
	Use:   "forget",
	Short: "Drop the workspace assignment without deleting anything",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		if err := c.ForgetWorkspace(); err != nil {
			return err
		}
		fmt.Println("Workspace forgotten.")
		printOthers(c)
		return nil
	},
}

var workspaceLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List the workspaces of the Zerops account, with the ones this machine holds a key for",
	Long:  "Every Sandbox Workspace service (zbox@1) of the organisation is a workspace. A row with a name has its key stored here and can be switched to; the others need `zbox workspace authorize <service-id>` first. Workspaces assigned with `zbox workspace use` that are not in the account are listed after.",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		zc, err := zeropsClient(c)
		if err != nil {
			return emit(c.Workspaces, func() {
				printWorkspaces(c)
				fmt.Println("Not logged in, only the workspaces known here are listed. Run `zbox login` to see the account's.")
			})
		}
		if c.ClientId == "" {
			user, err := zc.UserInfo(cmd.Context())
			if err != nil {
				return err
			}
			c.ClientId = user.ClientId
		}
		stacks, err := zc.Workspaces(cmd.Context(), c.ClientId)
		if err != nil {
			return err
		}
		return emit(map[string]any{"account": stacks, "known": c.Workspaces}, func() {
			byService := map[string]config.Workspace{}
			for _, w := range c.Workspaces {
				if w.ServiceId != "" {
					byService[w.ServiceId] = w
				}
			}
			listed := map[string]bool{}
			table("  NAME\tHOSTNAME\tPROJECT\tSTATUS\tSERVICE ID\tKEY", func(w io.Writer) {
				for _, st := range stacks {
					known, ok := byService[st.ServiceId]
					listed[st.ServiceId] = true
					fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\t%s\n", mark(c, known.WorkspaceUrl), dash(known.Name), st.Hostname, st.ProjectName, st.Status, st.ServiceId, map[bool]string{true: "stored", false: "-"}[ok])
				}
				for _, ws := range c.Workspaces {
					if listed[ws.ServiceId] {
						continue
					}
					fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\tstored\n", mark(c, ws.WorkspaceUrl), ws.Name, ws.Host(), dash(ws.ProjectName), "not in this account", dash(ws.ServiceId))
				}
			})
		})
	},
}

// mark is the asterisk of the current workspace in listings.
func mark(c config.Config, workspaceUrl string) string {
	if workspaceUrl != "" && workspaceUrl == c.WorkspaceUrl {
		return "*"
	}
	return " "
}

func printWorkspaces(c config.Config) {
	if len(c.Workspaces) == 0 {
		fmt.Println("No workspaces known here, run `zbox setup` or `zbox workspace authorize <service-id>`.")
		return
	}
	table("  NAME\tAPI HOST\tPROJECT\tSERVICE ID", func(w io.Writer) {
		for _, ws := range c.Workspaces {
			fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\n", mark(c, ws.WorkspaceUrl), ws.Name, ws.Host(), dash(ws.ProjectName), dash(ws.ServiceId))
		}
	})
}

var workspaceAuthorizeCmd = &cobra.Command{
	Use:   "authorize <service-id>",
	Short: "Fetch the API key of a workspace through the Zerops GUI and store it here",
	Long:  "The workspace API key is a generated secret the public API does not reveal to a token. The Zerops GUI opens, the user confirms (the reveal asks for the password again), and the key is handed to the CLI on this machine without being shown. The workspace is stored under its hostname (`--name` overrides) and becomes the current one only when none is. `zbox workspace ls` lists the service ids.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		zc, err := zeropsClient(c)
		if err != nil {
			return err
		}
		w, err := authorizeWorkspace(cmd.Context(), &c, zc, args[0], flagWorkspaceName, func(s string) { fmt.Println(s) })
		if err != nil {
			return err
		}
		if c.WorkspaceUrl == w.WorkspaceUrl {
			fmt.Printf("Stored the key of workspace %s (%s), it is the current one.\n", w.Name, w.Host())
		} else {
			fmt.Printf("Stored the key of workspace %s (%s). `zbox switch %s` makes it the current one.\n", w.Name, w.Host(), w.Name)
		}
		return nil
	},
}

// authorizeWorkspace runs the workspace hand-over for the service and stores the result under the name (the service hostname when empty). The current workspace stays unless there is none. Shared with `zbox setup`, which lands here when the env read gives the generator placeholder and then switches to it.
func authorizeWorkspace(ctx context.Context, c *config.Config, zc *zerops.Client, serviceId, name string, print func(string)) (config.Workspace, error) {
	stack, err := zc.ServiceStack(ctx, serviceId)
	if err != nil {
		return config.Workspace{}, err
	}
	values, err := browser.Handover(ctx, c.EffectiveGuiUrl(), browser.IntentWorkspace, url.Values{"serviceStackId": {serviceId}}, 5*time.Minute, print)
	if err != nil {
		return config.Workspace{}, err
	}
	apiHost, apiKey := strings.TrimSuffix(values.Get("apiHost"), "/"), values.Get("apiKey")
	if apiHost == "" || apiKey == "" {
		return config.Workspace{}, errors.New("the Zerops GUI answered without the workspace host and key")
	}
	if got := values.Get("serviceStackId"); got != "" && got != serviceId {
		return config.Workspace{}, fmt.Errorf("the Zerops GUI answered for service %s, not %s", got, serviceId)
	}
	if name == "" {
		name = stack.Name.Native()
	}
	w := config.Workspace{Name: name, ProjectId: string(stack.ProjectId), ProjectName: stack.Project.Name.Native(), ServiceId: serviceId, WorkspaceUrl: apiHost, ApiKey: apiKey}
	if err := c.Remember(w); err != nil {
		return config.Workspace{}, err
	}
	return w, nil
}

var switchCmd = &cobra.Command{
	Use:   "switch [workspace]",
	Short: "Make another known workspace the current one",
	Long:  "The workspace is named by its name, API host (or a prefix of it) or service id. Without an argument the known workspaces are listed. Nothing is contacted, the key stored for it is used from the next command on.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return emit(c.Workspaces, func() { printWorkspaces(c) })
		}
		w, err := c.Lookup(args[0])
		if err != nil {
			return err
		}
		if err := c.Assign(w); err != nil {
			return err
		}
		return emit(w, func() { fmt.Printf("Switched to %s (%s).\n", w.Name, w.Host()) })
	},
}

func init() {
	workspacePolicyCmd.Flags().StringVarP(&flagPolicyFile, "file", "f", "", "JSON file with the new policy, - for stdin")
	workspaceUseCmd.Flags().StringVar(&flagWorkspaceKey, "api-key", "", "workspace API key")
	workspaceUseCmd.Flags().StringVar(&flagWorkspaceName, "name", "", "name for `zbox switch`, defaults to the first label of the API host")
	workspaceAuthorizeCmd.Flags().StringVar(&flagWorkspaceName, "name", "", "name for `zbox switch`, defaults to the service hostname")
	workspaceCmd.AddCommand(workspaceLsCmd, workspaceAuthorizeCmd, workspaceKeyCmd, workspacePolicyCmd, workspaceUseCmd, workspaceForgetCmd)
	rootCmd.AddCommand(workspaceCmd, openCmd, switchCmd)
}

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the console of the workspace in the browser, signed in",
	Long:  "The orchestrator serves the console at the root of the workspace API host. The URL carries the workspace API key once: `/console/login?apiKey=` sets the console session cookie and redirects to `/`, the same trade-off as a MEMBERS port URL. Without a browser, paste the key from `zbox workspace key` into the console's own login form.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		target := strings.TrimRight(ws.BaseUrl, "/") + "/console/login?apiKey=" + ws.ApiKey()
		fmt.Println(target)
		if err := browser.Open(target); err != nil {
			fmt.Println("Could not open a browser, open the URL manually.")
		}
		return nil
	},
}

var workspaceKeyCmd = &cobra.Command{
	Use:   "key",
	Short: "Print the workspace API key, for the console sign-in or another machine",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		fmt.Println(ws.ApiKey())
		return nil
	},
}
