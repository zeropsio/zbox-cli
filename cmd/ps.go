package cmd

import (
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

var psCmd = &cobra.Command{
	Use:   "ps <id|name>",
	Short: "List the processes running in a sandbox",
	Long:  "Reads /proc of a running sandbox through the API, kernel threads left out, by pid. A sandbox that is not running lists nothing and is not woken.",
	Example: `  zbox ps app
  zbox ps app --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Processes(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Pid < list[j].Pid })
		return emit(list, func() {
			tb := newTable("PID", "USER", "STATE", "MEMORY", "CPU S", "RUNNING FOR", "COMMAND")
			for _, p := range list {
				user := fmt.Sprint(p.Uid)
				if p.Uid == 0 {
					user = "root"
				}
				command := p.Args
				if command == "" {
					command = p.Command
				}
				tb.row(fmt.Sprint(p.Pid), user, p.State, bytesString(p.RssBytes), fmt.Sprintf("%.1f", p.CpuSeconds), coarse(time.Duration(p.ElapsedSeconds)*time.Second), command)
			}
			tb.ifEmpty("No processes: the sandbox is not running.")
			tb.print()
		})
	},
}

func init() {
	sandboxArgs(psCmd)
	rootCmd.AddCommand(psCmd)
}
