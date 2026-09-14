package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var policyCmd = &cobra.Command{
	Use:   "policy <id>",
	Short: "Print a sandbox's policy, or replace it with -f",
	Long:  "The policy is versioned: the file must carry the policyGeneration that was read, a concurrent change is rejected instead of overwritten. Without -f the current one is printed in the shape -f takes.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		if flagPolicyFile == "" {
			s, err := ws.Sandbox(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printJson(api.RequestPutSandboxPolicy{Policy: s.Policy, PolicyGeneration: s.PolicyGeneration})
		}
		var body api.RequestPutSandboxPolicy
		if err := readJson(flagPolicyFile, &body); err != nil {
			return err
		}
		s, err := ws.PutPolicy(cmd.Context(), args[0], body.PolicyGeneration, body.Policy)
		if err != nil {
			return err
		}
		return emit(s, func() { fmt.Printf("Policy of %s updated to generation %d.\n", s.Id, s.PolicyGeneration) })
	},
}

// A sandbox exposes nothing until a port is declared in its ingress policy, and that can happen at any point in its life, so this is a command of its own rather than only a flag of `zbox new`.
var portCmd = &cobra.Command{
	Use:   "port",
	Short: "Open and close the ports a sandbox exposes",
}

var portLsCmd = &cobra.Command{
	Use:   "ls <id>",
	Short: "List the ports of a sandbox with their URLs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		s, err := ws.Sandbox(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(declaredPorts(s), func() { printPorts(s) })
	},
}

var portOpenCmd = &cobra.Command{
	Use:   "open <id> <port[/protocol][/access]>...",
	Short: "Declare ports on a sandbox, keeping the ones already there",
	Long:  "A port is port[/protocol][/access]: 3000, 3000/public, 5432/tcp/project. Access is members (default, the workspace API key), project, ticket, token or public. A request wakes a parked sandbox on every mode but public, wake and no-wake override that. Nothing restarts, the policy change applies on the next request.",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ports, err := parsePorts(args[1:])
		if err != nil {
			return err
		}
		return changePorts(cmd, args[0], func(current []api.BaseIngressPort) []api.BaseIngressPort { return mergePorts(current, ports) })
	},
}

var portCloseCmd = &cobra.Command{
	Use:   "close <id> <port>...",
	Short: "Remove ports from a sandbox's policy",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		closing := map[int]bool{}
		for _, a := range args[1:] {
			n, err := strconv.Atoi(a)
			if err != nil {
				return fmt.Errorf("%q is not a port number", a)
			}
			closing[n] = true
		}
		return changePorts(cmd, args[0], func(current []api.BaseIngressPort) []api.BaseIngressPort {
			var next []api.BaseIngressPort
			for _, p := range current {
				if !closing[p.Port] {
					next = append(next, p)
				}
			}
			return next
		})
	},
}

// changePorts reads the policy, edits its port list and writes it back under the generation it read, so a concurrent edit is rejected rather than merged into a list neither caller asked for.
func changePorts(cmd *cobra.Command, id string, edit func([]api.BaseIngressPort) []api.BaseIngressPort) error {
	ws, err := workspace()
	if err != nil {
		return err
	}
	s, err := ws.Sandbox(cmd.Context(), id)
	if err != nil {
		return err
	}
	policy := s.Policy
	if policy.Ingress == nil {
		policy.Ingress = &api.BaseIngress{}
	}
	policy.Ingress.Ports = edit(policy.Ingress.Ports)
	if policy.Ingress.Ports == nil {
		policy.Ingress.Ports = []api.BaseIngressPort{}
	}
	s, err = ws.PutPolicy(cmd.Context(), id, s.PolicyGeneration, policy)
	if err != nil {
		return err
	}
	return emit(declaredPorts(s), func() { printPorts(s) })
}

// parsePorts reads port[/protocol][/access] specs. The tokens after the port are recognised by value, so the order does not matter.
func parsePorts(specs []string) ([]api.BaseIngressPort, error) {
	var out []api.BaseIngressPort
	for _, spec := range specs {
		parts := strings.Split(spec, "/")
		n, err := strconv.Atoi(parts[0])
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%q is not a port number", parts[0])
		}
		p := api.BaseIngressPort{Port: n, Protocol: "HTTP", Access: api.BaseIngressPortAccessMEMBERS}
		for _, tok := range parts[1:] {
			switch strings.ToLower(tok) {
			case "http", "tcp":
				p.Protocol = api.BaseIngressPortProtocol(strings.ToUpper(tok))
			case "members", "project", "ticket", "token", "public":
				p.Access = api.BaseIngressPortAccess(strings.ToUpper(tok))
			case "ws", "websocket":
				t := true
				p.Websocket = &t
			case "wake", "no-wake":
				on := tok == "wake"
				p.WakeOnRequest = &on
			default:
				return nil, fmt.Errorf("%q in %q is not a protocol, an access mode, websocket, wake or no-wake", tok, spec)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// mergePorts replaces entries of the same port number and appends the rest, keeping the order of the existing list.
func mergePorts(current, add []api.BaseIngressPort) []api.BaseIngressPort {
	out := append([]api.BaseIngressPort(nil), current...)
	for _, p := range add {
		replaced := false
		for i := range out {
			if out[i].Port == p.Port && out[i].Protocol == p.Protocol {
				out[i] = p
				replaced = true
			}
		}
		if !replaced {
			out = append(out, p)
		}
	}
	return out
}

func init() {
	policyCmd.Flags().StringVarP(&flagPolicyFile, "file", "f", "", "JSON file with policy and policyGeneration, - for stdin")
	portCmd.AddCommand(portLsCmd, portOpenCmd, portCloseCmd)
	rootCmd.AddCommand(policyCmd, portCmd)
}
