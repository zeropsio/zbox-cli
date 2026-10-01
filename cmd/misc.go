package cmd

import (
	"errors"
	"fmt"
	"io"
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
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagCredPort == 0 {
			return errors.New("--port is required")
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
			for _, h := range s.Hosts {
				if h.Port == issued.Credential.Port {
					for _, p := range declaredPorts(s) {
						if p.Port == h.Port {
							open = openUrl(h.Url, p.Access, issued.Secret)
						}
					}
				}
			}
		}
		return emit(issued, func() {
			c := issued.Credential
			fields("ID", c.Id, "Kind", string(c.Kind), "Sandbox", c.SandboxId, "Port", fmt.Sprint(c.Port), "Name", dash(str(c.Name)), "Expires", when(c.Expires), "Secret", issued.Secret, "Open", dash(open))
			fmt.Println("The secret is not stored anywhere, copy it now. The URL carries it once, the router swaps it for a session cookie.")
		})
	},
}

var credentialLsCmd = &cobra.Command{
	Use:   "ls <sandbox-id|name>",
	Short: "List the credentials of a sandbox",
	Args:  cobra.ExactArgs(1),
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
			table("ID\tKIND\tPORT\tNAME\tEXPIRES\tUSED\tREVOKED", func(w io.Writer) {
				for _, c := range list {
					fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n", c.Id, c.Kind, c.Port, dash(str(c.Name)), when(c.Expires), whenPtr(c.UsedAt), whenPtr(c.RevokedAt))
				}
			})
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
		for _, id := range args {
			if err := ws.RevokeCredential(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Println("revoked", id)
		}
		return nil
	},
}

var webhookCmd = &cobra.Command{
	Use:   "webhook",
	Short: "Webhooks: signed event deliveries to your own URLs",
}

var webhookCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Register a webhook endpoint, the signing secret is shown once",
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagHookUrl == "" {
			return errors.New("--url is required")
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
			printWebhook(&created.Endpoint)
			fmt.Printf("Signing secret: %s\nIt is not shown again, copy it now.\n", created.SigningSecret)
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
	Use:   "ls",
	Short: "List webhook endpoints",
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
			table("ID\tURL\tENABLED\tEVENTS\tCREATED", func(w io.Writer) {
				for _, h := range list {
					fmt.Fprintf(w, "%s\t%s\t%v\t%s\t%s\n", h.Id, h.Url, h.Enabled, eventsText(h.Events, ","), when(h.Created))
				}
			})
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
	Use:   "rm <id>...",
	Short: "Delete webhook endpoints",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		for _, id := range args {
			if err := ws.DeleteWebhook(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Println("deleted", id)
		}
		return nil
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
			table("ID\tEVENT\tSANDBOX\tSTATUS\tATTEMPTS\tLAST CODE\tDELIVERED\tNEXT\tERROR", func(w io.Writer) {
				for _, d := range list {
					fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", d.Id, d.EventId, d.SandboxId, d.Status, d.Attempts, intPtr(d.LastResponseCode), whenPtr(d.Delivered), whenPtr(d.NextAttempt), dash(str(d.LastError)))
				}
			})
		})
	},
}

func printWebhook(h *api.BaseWebhookEndpoint) {
	fields("ID", h.Id, "URL", h.Url, "Enabled", fmt.Sprint(h.Enabled), "Events", eventsText(h.Events, ", "), "Created", when(h.Created))
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
			table("HOUR\tCPU s\tMEM AVG\tMEM MAX\tDISK\tNET RX\tNET TX\tROUTER REQ\tPROXY REQ", func(w io.Writer) {
				for _, m := range list {
					fmt.Fprintf(w, "%s\t%.1f\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n", when(m.Hour), m.CpuSeconds, bytesString(m.MemoryPssAvg), bytesString(m.MemoryPssMax), bytesString(m.DiskBytes), bytesString(m.NetRxBytes), bytesString(m.NetTxBytes), m.RouterRequests, m.ProxyRequests)
				}
			})
		})
	},
}

var quarantineCmd = &cobra.Command{
	Use:   "quarantine",
	Short: "Runtime objects the workspace found but does not know, kept rather than destroyed",
}

var quarantineLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List quarantined objects",
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
			table("ID\tKIND\tREF\tNOTE\tCREATED", func(w io.Writer) {
				for _, q := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", q.Id, q.Kind, q.Ref, dash(str(q.Note)), when(q.Created))
				}
			})
		})
	},
}

var quarantineRmCmd = &cobra.Command{
	Use:   "rm <id>...",
	Short: "Destroy quarantined objects",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		for _, id := range args {
			if err := ws.DeleteQuarantine(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Println("destroyed", id)
		}
		return nil
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
	rootCmd.AddCommand(credentialCmd, webhookCmd, metricsCmd, quarantineCmd)
}
