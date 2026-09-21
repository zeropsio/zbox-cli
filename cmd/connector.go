package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var flagConnectorKind string

var connectorCmd = &cobra.Command{
	Use:   "connector",
	Short: "Connectors: provider logins (claude, github, codex) whose credential the proxy injects for every sandbox",
	Long:  "A connector is a fixed catalogue entry: a Workspace env the credential lives in and the integrations the credential proxy serves with it. A connected connector makes `zbox new --integration <name>` hand the sandbox a proxied endpoint (base URL, git rewrite) with a placeholder token, so the real credential never enters the sandbox; `--secret <env>` delivers it instead and wins over the placeholder.",
}

var connectorLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List the connectors and whether they are connected",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Connectors(cmd.Context())
		if err != nil {
			return err
		}
		return emit(list, func() {
			table("NAME\tTITLE\tSTATUS\tKIND\tCREDENTIAL ENV\tINTEGRATIONS\tLOGIN", func(w io.Writer) {
				for _, c := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.Name, c.Title, c.Status, dash(str(c.CredentialKind)), c.CredentialEnv, strings.Join(c.Integrations, ","), yesNo(c.LoginAvailable))
				}
			})
		})
	},
}

var connectorGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show one connector",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		c, err := ws.Connector(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(c, func() { printConnector(c) })
	},
}

var connectorSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Store a credential for a connector, read from the terminal without echo or from stdin",
	Long:  "Stores an OAuth token or an API key as the connector's credential. The value is asked for without echo on a terminal, or read whole from stdin when it is a pipe (`gh auth token | zbox connector set github`); it is never taken from the command line. The kind is detected from the value unless --kind says.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		value, err := readCredential(args[0])
		if err != nil {
			return err
		}
		c, err := ws.SetConnectorCredential(cmd.Context(), args[0], api.RequestPutConnectorCredential{Value: value, Kind: optional(flagConnectorKind)})
		if err != nil {
			return err
		}
		return emit(c, func() { printConnector(c) })
	},
}

var connectorUnsetCmd = &cobra.Command{
	Use:   "unset <name>",
	Short: "Drop the credential of a connector, its integrations stay",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		c, err := ws.ClearConnectorCredential(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(c, func() { printConnector(c) })
	},
}

var connectorLoginCmd = &cobra.Command{
	Use:   "login <name>",
	Short: "Obtain the credential through the provider's browser login in a helper sandbox",
	Long:  "Starts the provider's login command in a helper sandbox and attaches this terminal to it. Complete the browser step it prints; the orchestrator harvests the credential from the terminal, stores it and terminates the helper sandbox, which closes the terminal. Needs a terminal.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("the login runs in this terminal and stdin is not one; pipe the credential into `zbox connector set` instead")
		}
		ws, err := workspace()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		login, err := ws.ConnectorLogin(ctx, args[0])
		if err != nil {
			return err
		}
		if flagJson {
			// The session is attached anyway, the JSON only names it for a `zbox shell --session` of one's own.
			if err := printJson(login); err != nil {
				return err
			}
		} else {
			fmt.Printf("Running `%s` in helper sandbox %s, follow its instructions. The terminal closes once the credential is stored.\n", login.Command, login.SandboxId)
		}
		// The session is detached and waiting with the reconnect grace running, so the attach must not dawdle.
		if err := attachShell(ctx, ws, login.SandboxId, login.SessionId, login.SessionTicket); err != nil {
			return err
		}
		c, err := ws.Connector(ctx, args[0])
		if err != nil {
			return err
		}
		if c.Status != "CONNECTED" {
			return fmt.Errorf("the login ended without a credential, %s is %s", c.Name, strings.ToLower(string(c.Status)))
		}
		return emit(c, func() { printConnector(c) })
	},
}

// readCredential takes the value without echo from a terminal, or whole from a pipe, never from argv where it would land in the shell history and the process list.
func readCredential(name string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		v, err := askSecret(fmt.Sprintf("Credential for %s", name))
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(v) == "" {
			return "", errors.New("no credential given")
		}
		return strings.TrimSpace(v), nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", errors.New("stdin was empty, pipe the credential in or run on a terminal")
	}
	return v, nil
}

func printConnector(c *api.BaseConnector) {
	fields("Name", c.Name, "Title", c.Title, "Status", string(c.Status), "Kind", dash(str(c.CredentialKind)), "Credential env", c.CredentialEnv, "Integrations", dash(strings.Join(c.Integrations, ", ")), "Guest env", dash(strings.Join(c.GuestEnv, ", ")), "Browser login", yesNo(c.LoginAvailable))
	if c.Status == "CONNECTED" {
		fmt.Printf("`zbox new --integration %s` gives a sandbox the proxied endpoint with a placeholder token.\n", strings.Join(c.Integrations, " --integration "))
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func init() {
	connectorSetCmd.Flags().StringVar(&flagConnectorKind, "kind", "", "OAUTH_TOKEN or API_KEY, detected from the value when omitted")
	connectorCmd.AddCommand(connectorLsCmd, connectorGetCmd, connectorSetCmd, connectorUnsetCmd, connectorLoginCmd)
	rootCmd.AddCommand(connectorCmd)
}
