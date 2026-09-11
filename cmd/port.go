package cmd

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

// A sandbox exposes nothing until a port is opened on it, and a port can be opened at any point in its life, so this is a command of its own rather than a flag of `zbox new`.
var portCmd = &cobra.Command{
	Use:   "port",
	Short: "Open and close the ports a sandbox exposes",
}

var portLsCmd = &cobra.Command{
	Use:   "ls <id>",
	Short: "List the ports open on a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		client, err := sandboxClient(c)
		if err != nil {
			return err
		}
		s, err := client.Get(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		printPorts(s)
		return nil
	},
}

var portOpenCmd = &cobra.Command{
	Use:   "open <id> <port>...",
	Short: "Open ports on a sandbox, keeping the ones already open",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return changePorts(cmd.Context(), args[0], args[1:], true)
	},
}

var portCloseCmd = &cobra.Command{
	Use:   "close <id> <port>...",
	Short: "Close ports on a sandbox",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return changePorts(cmd.Context(), args[0], args[1:], false)
	},
}

// changePorts reads the open set and writes it back changed, because the API replaces the whole set. Opening a port does not restart anything, it only tells the pool what it may route.
func changePorts(ctx context.Context, id string, args []string, open bool) error {
	ports, err := parsePorts(args)
	if err != nil {
		return err
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	client, err := sandboxClient(c)
	if err != nil {
		return err
	}
	s, err := client.Get(ctx, id)
	if err != nil {
		return err
	}
	wanted := map[int]bool{}
	for _, port := range s.PortNumbers() {
		wanted[port] = true
	}
	for _, port := range ports {
		wanted[port] = open
	}
	next := make([]int, 0, len(wanted))
	for port, keep := range wanted {
		if keep {
			next = append(next, port)
		}
	}
	s, err = client.SetPorts(ctx, id, next)
	if err != nil {
		return err
	}
	printPorts(s)
	return nil
}

func parsePorts(args []string) ([]int, error) {
	out := make([]int, 0, len(args))
	for _, arg := range args {
		port, err := strconv.Atoi(arg)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%q is not a port number", arg)
		}
		out = append(out, port)
	}
	return out, nil
}

func init() {
	portCmd.AddCommand(portLsCmd, portOpenCmd, portCloseCmd)
	rootCmd.AddCommand(portCmd)
}
