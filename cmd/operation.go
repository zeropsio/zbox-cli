package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagOpSandbox string
	flagOpStatus  string
	flagOpKind    string
	flagEvSandbox string
	flagEvAfter   int64
	flagEvLimit   int
	flagEvFollow  bool
)

// sleep waits the given seconds or until the command is interrupted.
func sleep(cmd *cobra.Command, secs int) error {
	select {
	case <-cmd.Context().Done():
		return cmd.Context().Err()
	case <-time.After(time.Duration(secs) * time.Second):
		return nil
	}
}

var opCmd = &cobra.Command{
	Use:     "operation",
	Aliases: []string{"op"},
	Short:   "Inspect the operations mutations return",
}

var opLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List operations",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		p := api.GetOperationListParams{SandboxId: optional(flagOpSandbox), Status: upperPtr[api.GetOperationListParamsStatus](flagOpStatus), Kind: upperPtr[api.GetOperationListParamsKind](flagOpKind)}
		list, err := ws.Operations(cmd.Context(), p)
		if err != nil {
			return err
		}
		return emit(list, func() {
			table("ID\tKIND\tSANDBOX\tSTATUS\tCREATED\tFINISHED\tERROR", func(w io.Writer) {
				for _, op := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", op.Id, op.Kind, dash(str(op.SandboxId)), op.Status, when(op.Created), whenPtr(op.Finished), dash(str(op.Error)))
				}
			})
		})
	},
}

var opGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one operation with its result",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		op, err := ws.Operation(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(op, func() { printOperation(op) })
	},
}

var opWaitCmd = &cobra.Command{
	Use:   "wait <id>",
	Short: "Wait for an operation to finish, exit non-zero when it fails",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		op, err := ws.WaitOperation(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(op, func() { printOperation(op) })
	},
}

func printOperation(op *api.BaseOperation) {
	pairs := []string{"ID", op.Id, "Kind", string(op.Kind), "Status", string(op.Status), "Sandbox", dash(str(op.SandboxId)), "Created", when(op.Created), "Finished", whenPtr(op.Finished)}
	if op.ParentId != nil {
		pairs = append(pairs, "Parent", *op.ParentId)
	}
	if op.Error != nil {
		pairs = append(pairs, "Error", *op.Error)
	}
	fields(pairs...)
	if op.Result != nil {
		fmt.Println("Result:")
		_ = printJson(op.Result)
	}
}

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "List the event log, or follow it live with --follow",
	Long:  "Without --follow the list is one long poll: it returns what is newer than --after, waiting up to 30 seconds for something new. With --follow the WebSocket stream is printed until interrupted.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		var after *int64
		if flagEvAfter > 0 {
			after = &flagEvAfter
		}
		if flagEvFollow {
			return ws.StreamEvents(cmd.Context(), after, flagEvSandbox, func(ev api.BaseEvent) error {
				if flagJson {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(ev)
				}
				fmt.Println(eventLine(ev))
				return nil
			})
		}
		p := api.GetEventListParams{AfterId: after, SandboxId: optional(flagEvSandbox), Limit: optionalInt(flagEvLimit)}
		if after != nil {
			p.WaitSeconds = optionalInt(30)
		}
		list, err := ws.Events(cmd.Context(), p)
		if err != nil {
			return err
		}
		return emit(list, func() {
			for _, ev := range list {
				fmt.Println(eventLine(ev))
			}
		})
	},
}

func eventLine(ev api.BaseEvent) string {
	line := fmt.Sprintf("%-8d %s %s %s", ev.Id, when(ev.Created), ev.SandboxId, ev.Type)
	if ev.FromStatus != nil || ev.ToStatus != nil {
		from, to := "", ""
		if ev.FromStatus != nil {
			from = string(*ev.FromStatus)
		}
		if ev.ToStatus != nil {
			to = string(*ev.ToStatus)
		}
		line += fmt.Sprintf(" %s>%s", from, to)
	}
	if ev.OperationId != nil {
		line += " op=" + *ev.OperationId
	}
	if ev.Payload != nil && len(*ev.Payload) > 0 {
		data, _ := json.Marshal(ev.Payload)
		line += " " + strings.TrimSpace(string(data))
	}
	return line
}

func init() {
	opLsCmd.Flags().StringVar(&flagOpSandbox, "sandbox", "", "only operations on this sandbox")
	opLsCmd.Flags().StringVar(&flagOpStatus, "status", "", "only operations in this status: pending, running, finished, failed")
	opLsCmd.Flags().StringVar(&flagOpKind, "kind", "", "only operations of this kind, for example wake or fork")
	opCmd.AddCommand(opLsCmd, opGetCmd, opWaitCmd)

	eventsCmd.Flags().StringVar(&flagEvSandbox, "sandbox", "", "only events of this sandbox")
	eventsCmd.Flags().Int64Var(&flagEvAfter, "after", 0, "only events with a greater id, the workspace-wide cursor")
	eventsCmd.Flags().IntVar(&flagEvLimit, "limit", 0, "at most this many")
	eventsCmd.Flags().BoolVarP(&flagEvFollow, "follow", "f", false, "stream events as they happen")
	rootCmd.AddCommand(opCmd, eventsCmd)
}
