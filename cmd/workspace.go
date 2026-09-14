package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagWorkspaceKey string
	flagPolicyFile   string
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
			return errors.New("--api-key is required, it is the apiKey env of the workspace service")
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
		c.ProjectId, c.ProjectName, c.ServiceId = "", "", ""
		c.WorkspaceUrl, c.ApiKey = url, flagWorkspaceKey
		if err := c.Save(); err != nil {
			return err
		}
		fmt.Printf("Using workspace %s (%s on %s).\n", url, info.Version, strings.ToLower(string(info.Substrate)))
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
		fmt.Println("Workspace unassigned.")
		return nil
	},
}

func init() {
	workspacePolicyCmd.Flags().StringVarP(&flagPolicyFile, "file", "f", "", "JSON file with the new policy, - for stdin")
	workspaceUseCmd.Flags().StringVar(&flagWorkspaceKey, "api-key", "", "workspace API key")
	workspaceCmd.AddCommand(workspacePolicyCmd, workspaceUseCmd, workspaceForgetCmd)
	rootCmd.AddCommand(workspaceCmd)
}
