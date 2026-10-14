package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagCredKind     string
	flagCredPort     int
	flagCredTtl      time.Duration
	flagCredName     string
	flagCredRevoked  bool
	flagHookUrl      string
	flagHookEvents   []string
	flagHookDisabled bool
	flagHookEnabled  bool
	flagDelivStatus  string
	flagDelivLimit   int
	flagMetricFrom   string
	flagMetricTo     string
)

var credentialCmd = &cobra.Command{
	Use:     "credential",
	Aliases: []string{"cred"},
	Short:   "Ingress credentials: one-time tickets for members and ticket ports, tokens for token ports",
}

var credentialIssueCmd = &cobra.Command{
	Use:   "issue <sandbox-id|name>",
	Short: "Issue a ticket or a token for a port, the secret is shown once",
	Example: `  zbox cred issue app --port 3000
  zbox cred issue app --port 3000 --kind token --ttl 1h`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagCredPort == 0 {
			return usagef("--port is required")
		}
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPostIngressCredential{Kind: upper[api.RequestPostIngressCredentialKind](flagCredKind), Port: flagCredPort, Name: optional(flagCredName), TtlSeconds: seconds(flagCredTtl)}
		issued, err := ws.IssueCredential(cmd.Context(), args[0], req)
		if err != nil {
			return err
		}
		open := ""
		if s, err := ws.Sandbox(cmd.Context(), args[0]); err == nil {
			for _, p := range declaredPorts(s) {
				if p.Port == issued.Credential.Port {
					open = openUrl(portUrl(s, p.Port), p.Access, issued.Secret)
				}
			}
		}
		return emit(issued, func() {
			// The secret is shown only now, so it is what --quiet keeps, not the credential id.
			if flagQuiet {
				fmt.Println(issued.Secret)
				return
			}
			c := issued.Credential
			fields("ID", c.Id, "Kind", string(c.Kind), "Sandbox", c.SandboxId, "Port", fmt.Sprint(c.Port), "Name", dash(str(c.Name)), "Expires", when(c.Expires), "Secret", issued.Secret, "Open", dash(open))
			note("The secret is shown only now. The Open URL carries it once, the router swaps it for a session cookie.")
		})
	},
}

var credentialLsCmd = &cobra.Command{
	Use:     "list <sandbox-id|name>",
	Aliases: []string{"ls"},
	Short:   "List the credentials of a sandbox",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Credentials(cmd.Context(), args[0], flagCredRevoked)
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "KIND", "PORT", "NAME", "EXPIRES", "USED", "REVOKED")
			for _, c := range list {
				tb.row(c.Id, c.Kind, c.Port, dash(str(c.Name)), when(c.Expires), whenPtr(c.UsedAt), whenPtr(c.RevokedAt))
			}
			tb.ifEmpty("No credentials. `zbox cred issue %s --port <port>` issues one.", args[0])
			tb.print()
		})
	},
}

var credentialRevokeCmd = &cobra.Command{
	Use:   "revoke <credential-id>...",
	Short: "Revoke credentials, effective on the next request",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		return deleteEach("", "", args, "revoked", func(i int) (*api.BaseOperation, error) {
			return nil, ws.RevokeCredential(cmd.Context(), args[i])
		})
	},
}

var webhookCmd = &cobra.Command{
	Use:   "webhook",
	Short: "Webhooks: signed event deliveries to your own URLs",
}

var webhookCreateCmd = &cobra.Command{
	Use:     "create",
	Short:   "Register a webhook endpoint, the signing secret is shown once",
	Example: `  zbox webhook create --url https://example.com/hook --event ready --event failed`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagHookUrl == "" {
			return usagef("--url is required")
		}
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPostWebhook{Url: flagHookUrl, Events: eventList(flagHookEvents)}
		if flagHookDisabled {
			f := false
			req.Enabled = &f
		}
		created, err := ws.CreateWebhook(cmd.Context(), req)
		if err != nil {
			return err
		}
		return emit(created, func() {
			if flagQuiet {
				fmt.Println(created.SigningSecret)
				return
			}
			printWebhook(&created.Endpoint, "Signing secret", created.SigningSecret)
			note("The signing secret is shown only now.")
		})
	},
}

var webhookSetCmd = &cobra.Command{
	Use:   "set <id>",
	Short: "Change a webhook endpoint",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPutWebhook{Url: optional(flagHookUrl)}
		if len(flagHookEvents) > 0 {
			events := eventList(flagHookEvents)
			req.Events = &events
		}
		if flagHookEnabled {
			t := true
			req.Enabled = &t
		}
		if flagHookDisabled {
			f := false
			req.Enabled = &f
		}
		h, err := ws.UpdateWebhook(cmd.Context(), args[0], req)
		if err != nil {
			return err
		}
		return emit(h, func() { printWebhook(h) })
	},
}

var webhookLsCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List webhook endpoints",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Webhooks(cmd.Context())
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "URL", "ENABLED", "EVENTS", "CREATED")
			for _, h := range list {
				tb.row(h.Id, h.Url, yesNo(h.Enabled), eventsText(h.Events, ","), when(h.Created))
			}
			tb.ifEmpty("No webhooks. `zbox webhook create --url <url>` registers one.")
			tb.print()
		})
	},
}

var webhookGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one webhook endpoint",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		h, err := ws.Webhook(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(h, func() { printWebhook(h) })
	},
}

var webhookRmCmd = &cobra.Command{
	Use:     "remove <id>...",
	Aliases: []string{"rm"},
	Short:   "Delete webhook endpoints",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		return deleteEach("delete webhook "+strings.Join(args, ", "), "", args, "deleted", func(i int) (*api.BaseOperation, error) {
			return nil, ws.DeleteWebhook(cmd.Context(), args[i])
		})
	},
}

var webhookDeliveriesCmd = &cobra.Command{
	Use:   "deliveries <id>",
	Short: "Show the delivery log of a webhook endpoint",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		p := api.GetWebhookDeliveryListParams{Status: upperPtr[api.GetWebhookDeliveryListParamsStatus](flagDelivStatus), Limit: optionalInt(flagDelivLimit)}
		list, err := ws.WebhookDeliveries(cmd.Context(), args[0], p)
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "EVENT", "SANDBOX", "STATUS", "ATTEMPTS", "LAST CODE", "DELIVERED", "NEXT", "ERROR")
			for _, d := range list {
				tb.row(d.Id, d.EventId, d.SandboxId, d.Status, d.Attempts, intPtr(d.LastResponseCode), whenPtr(d.Delivered), whenPtr(d.NextAttempt), dash(firstLine(str(d.LastError))))
			}
			tb.ifEmpty("No deliveries.")
			tb.print()
		})
	},
}

func printWebhook(h *api.BaseWebhookEndpoint, extra ...string) {
	fields(append([]string{"ID", h.Id, "URL", h.Url, "Enabled", yesNo(h.Enabled), "Events", eventsText(h.Events, ", "), "Created", when(h.Created)}, extra...)...)
}

// eventList turns the --event values into the request list: an empty list (no flag, or `all`) subscribes to every event.
func eventList(in []string) []string {
	out := []string{}
	for _, s := range in {
		s = strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
		if s == "ALL" {
			return []string{}
		}
		out = append(out, s)
	}
	return out
}

func eventsText(events []string, sep string) string {
	if len(events) == 0 {
		return "all"
	}
	return strings.Join(events, sep)
}

var metricsCmd = &cobra.Command{
	Use:   "metrics <sandbox-id|name>",
	Short: "Hourly resource usage of a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Metrics(cmd.Context(), args[0], api.GetSandboxMetricListParams{From: optional(flagMetricFrom), To: optional(flagMetricTo)})
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("HOUR", "CPU SECONDS", "MEM AVG", "MEM MAX", "DISK", "NET RX", "NET TX", "ROUTER REQ", "PROXY REQ")
			for _, m := range list {
				tb.row(when(m.Hour), fmt.Sprintf("%.1f", m.CpuSeconds), bytesString(m.MemoryPssAvg), bytesString(m.MemoryPssMax), bytesString(m.DiskBytes), bytesString(m.NetRxBytes), bytesString(m.NetTxBytes), m.RouterRequests, m.ProxyRequests)
			}
			tb.ifEmpty("No metrics in the range yet, they are recorded per hour.")
			tb.print()
		})
	},
}

var quarantineCmd = &cobra.Command{
	Use:   "quarantine",
	Short: "Runtime objects the workspace found but does not know, kept rather than destroyed",
}

var quarantineLsCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List quarantined objects",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Quarantine(cmd.Context())
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "KIND", "REF", "NOTE", "CREATED")
			for _, q := range list {
				tb.row(q.Id, q.Kind, q.Ref, dash(str(q.Note)), when(q.Created))
			}
			tb.ifEmpty("Nothing in quarantine.")
			tb.print()
		})
	},
}

var quarantineRmCmd = &cobra.Command{
	Use:     "remove <id>...",
	Aliases: []string{"rm"},
	Short:   "Destroy quarantined objects",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		return deleteEach("destroy quarantined "+strings.Join(args, ", "), "", args, "destroyed", func(i int) (*api.BaseOperation, error) {
			return nil, ws.DeleteQuarantine(cmd.Context(), args[i])
		})
	},
}

func init() {
	credentialIssueCmd.Flags().StringVar(&flagCredKind, "kind", "ticket", "ticket (one use, exchanged for a session cookie; members and ticket ports) or token (bearer; token ports only)")
	credentialIssueCmd.Flags().IntVar(&flagCredPort, "port", 0, "the sandbox port the credential is for")
	credentialIssueCmd.Flags().DurationVar(&flagCredTtl, "ttl", 0, "validity, for example 1h")
	credentialIssueCmd.Flags().StringVar(&flagCredName, "name", "", "label")
	credentialLsCmd.Flags().BoolVar(&flagCredRevoked, "include-revoked", false, "also list revoked ones")
	credentialCmd.AddCommand(credentialIssueCmd, credentialLsCmd, credentialRevokeCmd)

	for _, c := range []*cobra.Command{webhookCreateCmd, webhookSetCmd} {
		c.Flags().StringVar(&flagHookUrl, "url", "", "endpoint URL, reachable under the workspace's own egress")
		c.Flags().StringArrayVar(&flagHookEvents, "event", nil, "event type to deliver, repeatable, none or `all` means every event: created, ready, parked, woke, forked, restored, snapshot_taken, policy_changed, secrets_stale, exported, failed, archived, deleted, session_opened, session_closed, hook_finished")
		c.Flags().BoolVar(&flagHookDisabled, "disabled", false, "do not deliver")
	}
	webhookSetCmd.Flags().BoolVar(&flagHookEnabled, "enabled", false, "deliver again")
	webhookDeliveriesCmd.Flags().StringVar(&flagDelivStatus, "status", "", "pending, delivered or abandoned")
	webhookDeliveriesCmd.Flags().IntVar(&flagDelivLimit, "limit", 0, "at most this many")
	webhookCmd.AddCommand(webhookCreateCmd, webhookSetCmd, webhookLsCmd, webhookGetCmd, webhookRmCmd, webhookDeliveriesCmd)

	metricsCmd.Flags().StringVar(&flagMetricFrom, "from", "", "start of the range, RFC 3339")
	metricsCmd.Flags().StringVar(&flagMetricTo, "to", "", "end of the range, RFC 3339")
	quarantineCmd.AddCommand(quarantineLsCmd, quarantineRmCmd)
	destructive(webhookRmCmd, quarantineRmCmd)
	rootCmd.AddCommand(credentialCmd, webhookCmd, metricsCmd, quarantineCmd)
}
