package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagTokenName    string
	flagTokenScopes  []string
	flagTokenExpires time.Duration
	flagTokenRevoked bool
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Scoped API tokens: a bearer for a backend or a script that may call only part of the API",
	Long: "A token is sent as `Authorization: Bearer <token>` like the workspace API key, on the workspace's own API host or the project network. " +
		"Its scopes decide which routes it may call, any other route answers forbidden. Only the workspace key or your Zerops login manages tokens, a token never does.\n\n" +
		"Scopes: admin (every route but tokens), sandbox (lifecycle, exec, files, shell, ssh, snapshots, ports, attachments, lineages, export, import, " +
		"the archive download; implies sandbox:read), sandbox:read (sandboxes, operations, events, lineages and exports, and the bases and templates " +
		"they are made from), base (bases and templates), " +
		"integration (integrations and connectors), webhook, workspace (policy, domains, quarantine). Every token reads `zbox workspace`.\n\n" +
		"A sandbox token can deliver Workspace envs into its sandboxes and read them there, within the secrets ceiling of the workspace policy " +
		"(`allowedModes`, `allowedNames`), which only the workspace scope changes.",
}

var tokenCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a token, the secret is shown once",
	Example: `  zbox token create --name backend --scope sandbox
  zbox token create --name ci --scope sandbox --scope base --expires 720h
  token=$(zbox token create -q --name agent --scope sandbox)
  zbox workspace use https://<api-host> --api-key "$token"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagTokenName == "" {
			return usagef("--name is required")
		}
		if len(flagTokenScopes) == 0 {
			return usagef("--scope is required, for example --scope sandbox")
		}
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPostToken{Name: flagTokenName, Scopes: scopeList(flagTokenScopes), TtlSeconds: seconds(flagTokenExpires)}
		issued, err := ws.CreateToken(cmd.Context(), req)
		if err != nil {
			return err
		}
		return emit(issued, func() {
			// The secret is shown only now, so it is what --quiet keeps, not the token id.
			if flagQuiet {
				fmt.Println(issued.Secret)
				return
			}
			t := issued.Token
			fields("ID", t.Id, "Name", t.Name, "Scopes", strings.Join(t.Scopes, ", "), "Expires", expiresText(t.Expires), "Secret", issued.Secret)
			note("The secret is shown only now. Send it as `Authorization: Bearer <secret>`, or store it with `zbox workspace use <api-url> --api-key <secret>`.")
		})
	},
}

var tokenLsCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List the tokens of the workspace, newest first",
	Long:    "Lists every token that is not revoked, expired ones included. --include-revoked adds the revoked ones.",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Tokens(cmd.Context(), flagTokenRevoked)
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "NAME", "SCOPES", "EXPIRES", "LAST USED", "REVOKED", "CREATED")
			for _, t := range list {
				tb.row(t.Id, t.Name, strings.Join(t.Scopes, ","), expiresText(t.Expires), whenPtr(t.LastUsedAt), whenPtr(t.RevokedAt), when(t.Created))
			}
			tb.ifEmpty("No tokens. `zbox token create --name <name> --scope sandbox` creates one.")
			tb.print()
		})
	},
}

var tokenRevokeCmd = &cobra.Command{
	Use:   "revoke <token-id>...",
	Short: "Revoke tokens, effective on the next request",
	Long:  "The next request with a revoked token answers unauthorized. A shell or an event stream it opened stays open until it closes.",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		return deleteEach("", "", args, "revoked", func(i int) (*api.BaseOperation, error) {
			return nil, ws.RevokeToken(cmd.Context(), args[i])
		})
	},
}

// scopeList takes repeated and comma-separated --scope values in any case.
func scopeList(in []string) []string {
	out := []string{}
	for _, value := range in {
		for _, scope := range strings.Split(value, ",") {
			if scope = strings.ToLower(strings.TrimSpace(scope)); scope != "" {
				out = append(out, scope)
			}
		}
	}
	return out
}

func expiresText(expires *string) string {
	if expires == nil {
		return "never"
	}
	return when(*expires)
}

func init() {
	tokenCreateCmd.Flags().StringVar(&flagTokenName, "name", "", "what the token is for")
	tokenCreateCmd.Flags().StringArrayVar(&flagTokenScopes, "scope", nil, "repeatable or comma-separated: admin, sandbox, sandbox:read, base, integration, webhook, workspace")
	tokenCreateCmd.Flags().DurationVar(&flagTokenExpires, "expires", 0, "valid for this long, for example 720h; without it the token never expires")
	tokenLsCmd.Flags().BoolVar(&flagTokenRevoked, "include-revoked", false, "also list revoked ones")
	tokenCmd.AddCommand(tokenCreateCmd, tokenLsCmd, tokenRevokeCmd)
	rootCmd.AddCommand(tokenCmd)
}
