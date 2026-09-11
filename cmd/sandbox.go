package cmd

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/zboxapi"
)

var (
	flagNewImage   string
	flagNewPorts   []int
	flagNewNoWait  bool
	flagExecDir    string
	flagExecTimout int
)

var newCmd = &cobra.Command{
	Use:   "new [name]",
	Short: "Create a sandbox",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		if err := ensurePool(cmd.Context(), &c); err != nil {
			return err
		}
		client, err := sandboxClient(c)
		if err != nil {
			return err
		}
		req := zboxapi.CreateRequest{}
		if len(args) == 1 {
			req.Name = &args[0]
		}
		if flagNewImage != "" {
			req.Image = &flagNewImage
		}
		req.Ports = flagNewPorts
		s, err := client.Create(cmd.Context(), req)
		if err != nil {
			return err
		}
		if !flagNewNoWait {
			s, err = client.WaitRunning(cmd.Context(), s.Id)
			if err != nil {
				return err
			}
		}
		printSandbox(s)
		if s.Status == "FAILED" {
			return errors.New("sandbox failed to start, check the orchestrator log")
		}
		return nil
	},
}

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List sandboxes",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		client, err := sandboxClient(c)
		if err != nil {
			return err
		}
		list, err := client.List(cmd.Context())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tIMAGE\tAGE\tPORTS")
		for _, s := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Id, s.Name, s.Status, s.Image, age(s.Created), portList(s))
		}
		return w.Flush()
	},
}

var getCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one sandbox",
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
		printSandbox(s)
		return nil
	},
}

var rmCmd = &cobra.Command{
	Use:   "rm <id>...",
	Short: "Delete sandboxes",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		client, err := sandboxClient(c)
		if err != nil {
			return err
		}
		for _, id := range args {
			if err := client.Delete(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Println("deleted", id)
		}
		return nil
	},
}

var execCmd = &cobra.Command{
	Use:   "exec <id> -- <command...>",
	Short: "Run a command in a sandbox and exit with its exit code",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		client, err := sandboxClient(c)
		if err != nil {
			return err
		}
		req := zboxapi.ExecRequest{Command: strings.Join(args[1:], " ")}
		if flagExecDir != "" {
			req.WorkingDir = &flagExecDir
		}
		if flagExecTimout != 0 {
			req.TimeoutSeconds = &flagExecTimout
		}
		res, err := client.Exec(cmd.Context(), args[0], req)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stdout, res.Stdout)
		fmt.Fprint(os.Stderr, res.Stderr)
		if res.ExitCode != 0 {
			return exitError{code: res.ExitCode}
		}
		return nil
	},
}

func printSandbox(s zboxapi.Sandbox) {
	fmt.Printf("ID:      %s\nName:    %s\nStatus:  %s\nImage:   %s\nCreated: %s\n",
		s.Id, s.Name, s.Status, s.Image, s.Created.Local().Format(time.RFC3339))
	printPorts(s)
}

// printPorts says what to do about an empty set instead of printing nothing, a sandbox with no port open is the normal starting state rather than a mistake.
func printPorts(s zboxapi.Sandbox) {
	if len(s.Ports) == 0 {
		fmt.Printf("Ports:   none, open one with `zbox port open %s <port>`\n", s.Id)
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for i, p := range s.Ports {
		label := "Ports:"
		if i > 0 {
			label = ""
		}
		fmt.Fprintf(w, "%s\t%d\t%s\n", label, p.Port, p.Url)
	}
	_ = w.Flush()
}

func portList(s zboxapi.Sandbox) string {
	if len(s.Ports) == 0 {
		return "-"
	}
	out := make([]string, 0, len(s.Ports))
	for _, p := range s.Ports {
		out = append(out, strconv.Itoa(p.Port))
	}
	return strings.Join(out, ",")
}

func age(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func init() {
	newCmd.Flags().StringVar(&flagNewImage, "image", "", "docker image instead of the default agent image")
	// Cobra reads backticks in a usage string as the name of the flag value, so the help text cannot quote a command.
	newCmd.Flags().IntSliceVarP(&flagNewPorts, "port", "p", nil, "port to expose on its own URL, repeatable, a sandbox starts with none and ports can be opened at any time later")
	newCmd.Flags().BoolVar(&flagNewNoWait, "no-wait", false, "return right after the create request")
	execCmd.Flags().StringVarP(&flagExecDir, "workdir", "w", "", "working directory inside the sandbox")
	execCmd.Flags().IntVar(&flagExecTimout, "timeout", 0, "command timeout in seconds")
	rootCmd.AddCommand(newCmd, lsCmd, getCmd, rmCmd, execCmd)
}
