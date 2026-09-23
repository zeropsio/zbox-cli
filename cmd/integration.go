package cmd

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagIntTarget      string
	flagIntCredential  string
	flagIntInject      string
	flagIntStripPrefix string
	flagIntRate        string
	flagAttachPeer     string
	flagAttachPeerPort int
	flagAttachInt      string
)

var integrationCmd = &cobra.Command{
	Use:     "integration",
	Aliases: []string{"int"},
	Short:   "Integrations: named targets the credential proxy calls on a sandbox's behalf, the credential never enters the sandbox",
}

// parseInject reads bearer, basic or header:<Name>.
func parseInject(s string) (*api.BaseInject, error) {
	if s == "" {
		return nil, nil
	}
	kind, header, _ := strings.Cut(s, ":")
	switch strings.ToLower(kind) {
	case "bearer", "basic":
		return &api.BaseInject{Type: api.BaseInjectType(strings.ToUpper(kind))}, nil
	case "header":
		if header == "" {
			return nil, errors.New("--inject header needs the header name, header:X-Api-Key")
		}
		return &api.BaseInject{Type: "HEADER", Header: &header}, nil
	}
	return nil, fmt.Errorf("%q is not bearer, basic or header:<name>", s)
}

// parseRate reads requests-per-second[/burst].
func parseRate(s string) (*api.BaseRate, error) {
	if s == "" {
		return nil, nil
	}
	rps, burst, _ := strings.Cut(s, "/")
	r, err := strconv.Atoi(rps)
	if err != nil {
		return nil, fmt.Errorf("%q is not requests-per-second[/burst]", s)
	}
	out := &api.BaseRate{RequestsPerSecond: r, Burst: r}
	if burst != "" {
		if out.Burst, err = strconv.Atoi(burst); err != nil {
			return nil, fmt.Errorf("%q is not requests-per-second[/burst]", s)
		}
	}
	return out, nil
}

var integrationCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Define an integration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagIntTarget == "" || flagIntCredential == "" || flagIntInject == "" {
			return errors.New("--target-url, --credential-env and --inject are required")
		}
		ws, err := workspace()
		if err != nil {
			return err
		}
		inject, err := parseInject(flagIntInject)
		if err != nil {
			return err
		}
		rate, err := parseRate(flagIntRate)
		if err != nil {
			return err
		}
		i, err := ws.CreateIntegration(cmd.Context(), api.RequestPostIntegration{Name: args[0], TargetUrl: flagIntTarget, CredentialEnv: flagIntCredential, Inject: *inject, RateLimit: rate, StripPrefix: optional(flagIntStripPrefix)})
		if err != nil {
			return err
		}
		return emit(i, func() { printIntegration(i) })
	},
}

var integrationSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Change an integration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		inject, err := parseInject(flagIntInject)
		if err != nil {
			return err
		}
		rate, err := parseRate(flagIntRate)
		if err != nil {
			return err
		}
		i, err := ws.UpdateIntegration(cmd.Context(), args[0], api.RequestPutIntegration{TargetUrl: optional(flagIntTarget), CredentialEnv: optional(flagIntCredential), Inject: inject, RateLimit: rate, StripPrefix: optional(flagIntStripPrefix)})
		if err != nil {
			return err
		}
		return emit(i, func() { printIntegration(i) })
	},
}

var integrationLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List integrations",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Integrations(cmd.Context())
		if err != nil {
			return err
		}
		return emit(list, func() {
			table("NAME\tTARGET\tCREDENTIAL ENV\tINJECT\tRATE\tCREATED", func(w io.Writer) {
				for _, i := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", i.Name, i.TargetUrl, i.CredentialEnv, injectString(i.Inject), rateString(i.RateLimit), when(i.Created))
				}
			})
		})
	},
}

var integrationGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show one integration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		i, err := ws.Integration(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(i, func() { printIntegration(i) })
	},
}

var integrationRmCmd = &cobra.Command{
	Use:   "rm <name>...",
	Short: "Delete integrations no sandbox has attached",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		for _, name := range args {
			if err := ws.DeleteIntegration(cmd.Context(), name); err != nil {
				return err
			}
			fmt.Println("deleted", name)
		}
		return nil
	},
}

func printIntegration(i *api.BaseIntegration) {
	fields("Name", i.Name, "Target", i.TargetUrl, "Credential env", i.CredentialEnv, "Inject", injectString(i.Inject), "Strip prefix", dash(str(i.StripPrefix)), "Rate", rateString(i.RateLimit), "Created", when(i.Created))
	fmt.Printf("Inside a sandbox: http://%s.int.<workspace-domain>/\n", i.Name)
}

func injectString(i api.BaseInject) string {
	out := strings.ToLower(string(i.Type))
	if i.Header != nil {
		out += ":" + *i.Header
	}
	return out
}

func rateString(r *api.BaseRate) string {
	if r == nil {
		return "-"
	}
	return fmt.Sprintf("%d/s burst %d", r.RequestsPerSecond, r.Burst)
}

// parseAttachment reads the --integration form of `zbox new`: name, or label=name, or label=peer:<sandbox>:<port>.
func parseAttachment(spec string) (api.RequestPostIntegrationAttachment, error) {
	label, target, found := strings.Cut(spec, "=")
	if !found {
		target = label
	}
	if strings.HasPrefix(target, "peer:") {
		parts := strings.Split(strings.TrimPrefix(target, "peer:"), ":")
		if len(parts) != 2 {
			return api.RequestPostIntegrationAttachment{}, fmt.Errorf("%q is not label=peer:<sandbox>:<port>", spec)
		}
		port, err := strconv.Atoi(parts[1])
		if err != nil {
			return api.RequestPostIntegrationAttachment{}, fmt.Errorf("%q is not a port number", parts[1])
		}
		if !found {
			return api.RequestPostIntegrationAttachment{}, errors.New("a peer attachment needs a label, label=peer:<sandbox>:<port>")
		}
		return api.RequestPostIntegrationAttachment{Name: label, PeerSandboxId: &parts[0], PeerPort: &port}, nil
	}
	return api.RequestPostIntegrationAttachment{Name: label, IntegrationName: &target}, nil
}

var attachCmd = &cobra.Command{
	Use:   "attach <sandbox-id|name> <label>",
	Short: "Attach an integration or a peer sandbox to a sandbox under a label",
	Long:  "The sandbox then reaches it at http://<label>.int.<workspace-domain>/. Give --integration for a defined integration or --peer with --peer-port for another sandbox's published port.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPostIntegrationAttachment{Name: args[1]}
		switch {
		case flagAttachInt != "" && flagAttachPeer != "":
			return errors.New("--integration and --peer are exclusive")
		case flagAttachInt != "":
			req.IntegrationName = &flagAttachInt
		case flagAttachPeer != "":
			if flagAttachPeerPort == 0 {
				return errors.New("--peer needs --peer-port")
			}
			req.PeerSandboxId, req.PeerPort = &flagAttachPeer, &flagAttachPeerPort
		default:
			// A bare label is the integration of the same name, the common case.
			req.IntegrationName = &args[1]
		}
		a, err := ws.Attach(cmd.Context(), args[0], req)
		if err != nil {
			return err
		}
		return emit(a, func() {
			fmt.Printf("%s attached to %s as http://%s.int.<workspace-domain>/\n", attachmentTarget(*a), a.SandboxId, a.Name)
		})
	},
}

var detachCmd = &cobra.Command{
	Use:   "detach <sandbox-id|name> <label>...",
	Short: "Detach integrations from a sandbox",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		for _, name := range args[1:] {
			if err := ws.Detach(cmd.Context(), args[0], name); err != nil {
				return err
			}
			fmt.Println("detached", name)
		}
		return nil
	},
}

var attachmentsCmd = &cobra.Command{
	Use:   "attachments <sandbox-id|name>",
	Short: "List what a sandbox has attached",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Attachments(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(list, func() {
			table("LABEL\tTARGET\tCREATED", func(w io.Writer) {
				for _, a := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\n", a.Name, attachmentTarget(a), when(a.Created))
				}
			})
		})
	},
}

func attachmentTarget(a api.BaseIntegrationAttachment) string {
	if a.PeerSandboxId != nil {
		return fmt.Sprintf("peer %s:%s", *a.PeerSandboxId, intPtr(a.PeerPort))
	}
	return "integration " + str(a.IntegrationName)
}

func init() {
	for _, c := range []*cobra.Command{integrationCreateCmd, integrationSetCmd} {
		c.Flags().StringVar(&flagIntTarget, "target-url", "", "base URL the proxy forwards to")
		c.Flags().StringVar(&flagIntCredential, "credential-env", "", "workspace env holding the credential, read on every request")
		c.Flags().StringVar(&flagIntInject, "inject", "", "how the credential is sent: bearer, basic or header:<name>")
		c.Flags().StringVar(&flagIntStripPrefix, "strip-prefix", "", "path prefix removed before forwarding")
		c.Flags().StringVar(&flagIntRate, "rate", "", "rate limit as requests-per-second[/burst]")
	}
	integrationCmd.AddCommand(integrationCreateCmd, integrationSetCmd, integrationLsCmd, integrationGetCmd, integrationRmCmd)

	attachCmd.Flags().StringVar(&flagAttachInt, "integration", "", "integration name, defaults to the label")
	attachCmd.Flags().StringVar(&flagAttachPeer, "peer", "", "id of the peer sandbox")
	attachCmd.Flags().IntVar(&flagAttachPeerPort, "peer-port", 0, "published port of the peer sandbox")
	rootCmd.AddCommand(integrationCmd, attachCmd, detachCmd, attachmentsCmd)
}
