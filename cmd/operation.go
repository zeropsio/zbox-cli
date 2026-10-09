package cmd

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagOpSandbox string
	flagOpStatus  string
	flagOpKind    string
	flagOpLimit   int
	flagOpAll     bool
	flagOpBefore  string
	flagEvSandbox string
	flagEvAfter   int64
	flagEvBefore  int64
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
	Short: "List operations, newest first",
	Long:  "The list is the newest 100 operations unless --limit says otherwise; --before <id> continues with older ones, --all pages back through every operation the workspace remembers.",
	Example: `  zbox operation ls --sandbox app --status failed
  zbox operation ls --kind wake --limit 10 -q`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		if err := resolveFlag(cmd, ws, &flagOpSandbox); err != nil {
			return err
		}
		p := api.GetOperationListParams{SandboxId: optional(flagOpSandbox), Status: upperPtr[api.GetOperationListParamsStatus](flagOpStatus), Kind: upperPtr[api.GetOperationListParamsKind](flagOpKind), BeforeId: optional(flagOpBefore)}
		list, err := listOperations(cmd, ws, p)
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "KIND", "SANDBOX", "STATUS", "TOOK", "CREATED", "ERROR")
			for _, op := range list {
				tb.row(op.Id, op.Kind, dash(str(op.SandboxId)), op.Status, took(op.Created, op.Finished), when(op.Created), dash(firstLine(str(op.Error))))
			}
			tb.ifEmpty("No operations match.")
			tb.print()
			if page := cmp.Or(flagOpLimit, opPageDefault); !flagOpAll && len(list) == page {
				morePages(list[len(list)-1].Id)
			}
		})
	},
}

// opPageMax is the largest page GET /operation-list answers, opPageDefault the one it answers without a limit.
const (
	opPageMax     = 1000
	opPageDefault = 100
)

// listOperations reads the newest operations: the server's default page with no --limit, otherwise as many as asked for, paging back with beforeId when that is more than one page or --all.
func listOperations(cmd *cobra.Command, ws *api.Workspace, p api.GetOperationListParams) ([]api.BaseOperation, error) {
	if flagOpLimit <= 0 && !flagOpAll {
		return ws.Operations(cmd.Context(), p)
	}
	var list []api.BaseOperation
	for flagOpAll || len(list) < flagOpLimit {
		page := opPageMax
		if !flagOpAll {
			page = min(page, flagOpLimit-len(list))
		}
		p.Limit = &page
		ops, err := ws.Operations(cmd.Context(), p)
		if err != nil {
			return nil, err
		}
		list = append(list, ops...)
		if len(ops) < page {
			break
		}
		p.BeforeId = &ops[len(ops)-1].Id
	}
	return list, nil
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
	Use:     "wait <id>",
	Short:   "Wait for an operation to finish, exit non-zero when it fails",
	Example: `  zbox operation wait <id> --timeout 5m`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		op, err := waitFor(cmd, ws, &api.BaseOperation{Id: args[0]})
		if err != nil {
			return err
		}
		return emit(op, func() { printOperation(op) })
	},
}

func printOperation(op *api.BaseOperation) {
	pairs := []string{"ID", op.Id, "Kind", string(op.Kind), "Status", string(op.Status), "Sandbox", dash(str(op.SandboxId)), "Created", when(op.Created), "Finished", whenPtr(op.Finished), "Took", took(op.Created, op.Finished)}
	if op.ParentId != nil {
		pairs = append(pairs, "Parent", *op.ParentId)
	}
	if op.Error != nil {
		pairs = append(pairs, "Error", *op.Error)
	}
	fields(pairs...)
	if op.Result != nil {
		section("Result", op.Result)
	}
}

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Show the newest events, or follow the log live with --follow",
	Long:  "With no cursor the newest events are printed, the latest at the bottom; --before continues with the ones older than an id. With --after the list is one long poll: it returns what is newer than the id, oldest first, waiting up to 30 seconds for something new. With --follow the WebSocket stream is printed until interrupted. --json prints the list in the order the API answers it, which is newest first unless --after is given.",
	Example: `  zbox events --sandbox app
  zbox events --follow --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		var after *int64
		if flagEvAfter > 0 {
			after = &flagEvAfter
		}
		if err := resolveFlag(cmd, ws, &flagEvSandbox); err != nil {
			return err
		}
		if flagEvFollow {
			// Without a cursor the stream would replay the whole log, a follow starts at the newest event instead.
			if after == nil {
				newest, limit := true, 1
				latest, err := ws.Events(cmd.Context(), api.GetEventListParams{NewestFirst: &newest, Limit: &limit})
				if err != nil {
					return err
				}
				var last int64
				if len(latest) > 0 {
					last = latest[0].Id
				}
				after = &last
			}
			return ws.StreamEvents(cmd.Context(), after, flagEvSandbox, func(ev api.BaseEvent) error {
				if flagJson {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(ev)
				}
				fmt.Println(eventLine(ev))
				return nil
			})
		}
		p := api.GetEventListParams{AfterId: after, SandboxId: optional(flagEvSandbox), Limit: optionalInt(flagEvLimit)}
		if after != nil && flagEvBefore > 0 {
			return usagef("--after and --before are two directions, give one")
		}
		if after != nil {
			p.WaitSeconds = optionalInt(30)
		} else {
			newest := true
			p.NewestFirst = &newest
			if flagEvBefore > 0 {
				p.BeforeId = &flagEvBefore
			}
		}
		list, err := ws.Events(cmd.Context(), p)
		if err != nil {
			return err
		}
		return emit(list, func() {
			// Newest first is turned around, so the text reads like a log whichever way it was asked for.
			if after == nil {
				slices.Reverse(list)
			}
			tb := newTable("ID", "SANDBOX", "TYPE", "CHANGE", "OPERATION", "PAYLOAD", "CREATED")
			for _, ev := range list {
				tb.row(ev.Id, ev.SandboxId, ev.Type, dash(eventChange(ev)), dash(str(ev.OperationId)), dash(eventPayload(ev)), when(ev.Created))
			}
			tb.ifEmpty("No events.")
			tb.print()
			if after == nil && len(list) > 0 {
				note("Older ones: --before %d.", list[0].Id)
			}
		})
	},
}

// eventLine is one event of --follow: a log line on a terminal, the table's columns tab-separated otherwise.
func eventLine(ev api.BaseEvent) string {
	at := when(ev.Created)
	if t := api.Time(ev.Created); pretty() && !t.IsZero() {
		at = t.Local().Format(time.DateTime)
	}
	cells := []string{fmt.Sprint(ev.Id), at, ev.SandboxId, string(ev.Type), dash(eventChange(ev)), dash(str(ev.OperationId)), dash(eventPayload(ev))}
	if !pretty() {
		return plainRow(cells)
	}
	return strings.Join(cells, "  ")
}

// eventChange is the status transition of an event, FROM>TO.
func eventChange(ev api.BaseEvent) string {
	if ev.FromStatus == nil && ev.ToStatus == nil {
		return ""
	}
	from, to := "", ""
	if ev.FromStatus != nil {
		from = string(*ev.FromStatus)
	}
	if ev.ToStatus != nil {
		to = string(*ev.ToStatus)
	}
	if from == "" {
		return to
	}
	return from + ">" + to
}

func eventPayload(ev api.BaseEvent) string {
	if ev.Payload == nil || len(*ev.Payload) == 0 {
		return ""
	}
	data, _ := json.Marshal(ev.Payload)
	return strings.TrimSpace(string(data))
}

func init() {
	opLsCmd.Flags().StringVar(&flagOpSandbox, "sandbox", "", "only operations on this sandbox")
	opLsCmd.Flags().StringVar(&flagOpStatus, "status", "", "only operations in this status: pending, running, finished, failed")
	opLsCmd.Flags().StringVar(&flagOpKind, "kind", "", "only operations of this kind, for example wake or fork")
	opLsCmd.Flags().IntVar(&flagOpLimit, "limit", 0, "at most this many, the newest 100 when omitted")
	opLsCmd.Flags().BoolVar(&flagOpAll, "all", false, "every operation, paged from the newest back")
	opLsCmd.Flags().StringVar(&flagOpBefore, "before", "", "only operations older than this one (the last id of the previous page)")
	timeoutFlag(opWaitCmd)
	opCmd.AddCommand(opLsCmd, opGetCmd, opWaitCmd)

	eventsCmd.Flags().StringVar(&flagEvSandbox, "sandbox", "", "only events of this sandbox")
	eventsCmd.Flags().Int64Var(&flagEvAfter, "after", 0, "only events with a greater id, oldest first, the workspace-wide cursor; with --follow, the stream replays from there instead of starting at the newest event")
	eventsCmd.Flags().Int64Var(&flagEvBefore, "before", 0, "only events with a smaller id, to page back from the newest")
	eventsCmd.Flags().IntVar(&flagEvLimit, "limit", 0, "at most this many")
	eventsCmd.Flags().BoolVarP(&flagEvFollow, "follow", "f", false, "stream events as they happen")
	rootCmd.AddCommand(opCmd, eventsCmd)
}
