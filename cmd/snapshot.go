package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagSnapMemory    bool
	flagRestoreName   string
	flagRestoreMemory bool
	flagLineageExport string
	flagExportDest    string
	flagExportLineage string
	flagImportSource  string
)

var snapshotCmd = &cobra.Command{
	Use:     "snapshot",
	Aliases: []string{"snap"},
	Short:   "Take, list and restore snapshots",
}

var snapshotCreateCmd = &cobra.Command{
	Use:     "create <sandbox-id|name> <name>",
	Short:   "Take a named snapshot of a sandbox's disk, optionally with its memory",
	Example: `  zbox snapshot create app before-upgrade --with-memory`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPostSandboxSnapshot{Name: args[1]}
		if flagSnapMemory {
			req.WithMemory = &flagSnapMemory
		}
		return runOperation(cmd, ws, "snapshot taken", func() (*api.BaseOperation, error) { return ws.TakeSnapshot(cmd.Context(), args[0], req) })
	},
}

var snapshotLsCmd = &cobra.Command{
	Use:     "list <sandbox-id|name>",
	Aliases: []string{"ls"},
	Short:   "List the snapshots of a sandbox",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Snapshots(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "NAME", "KIND", "MEMORY", "SIZE", "CREATED")
			for _, s := range list {
				tb.row(s.Id, dash(str(s.Name)), s.Kind, yesNo(s.HasMemory), bytesPtr(s.SizeBytes), when(s.Created))
			}
			tb.ifEmpty("No snapshots. `zbox snapshot create %s <name>` takes one.", args[0])
			tb.print()
		})
	},
}

var snapshotGetCmd = &cobra.Command{
	Use:   "get <snapshot-id>",
	Short: "Show one snapshot",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		s, err := ws.Snapshot(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(s, func() {
			fields("ID", s.Id, "Name", dash(str(s.Name)), "Sandbox", s.SandboxId, "Kind", string(s.Kind), "Memory", yesNo(s.HasMemory), "Size", bytesPtr(s.SizeBytes), "Created", when(s.Created))
		})
	},
}

var snapshotRmCmd = &cobra.Command{
	Use:     "remove <snapshot-id>...",
	Aliases: []string{"rm"},
	Short:   "Delete snapshots",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		return deleteEach("delete snapshot "+strings.Join(args, ", "), "", args, "deleted", func(i int) (*api.BaseOperation, error) {
			return nil, ws.DeleteSnapshot(cmd.Context(), args[i])
		})
	},
}

var snapshotRestoreCmd = &cobra.Command{
	Use:     "restore <snapshot-id>",
	Short:   "Restore a snapshot into a new sandbox, the original is never rolled back",
	Example: `  zbox snapshot restore $(zbox snapshot list app -q | head -1) --name app-2 --with-memory`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPostSnapshotRestore{Name: optional(flagRestoreName)}
		if flagRestoreMemory {
			req.WithMemory = &flagRestoreMemory
		}
		return runSpawn(cmd, ws, func() (*api.ResponseSpawn, error) { return ws.Restore(cmd.Context(), args[0], req) })
	},
}

var lineageCmd = &cobra.Command{
	Use:   "lineage",
	Short: "Lineages: a sandbox and everything forked or restored from it, the unit of export",
}

var lineageLsCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List lineages",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Lineages(cmd.Context())
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "CREATED")
			for _, l := range list {
				tb.row(l.Id, when(l.Created))
			}
			tb.ifEmpty("No lineages yet, every sandbox starts one.")
			tb.print()
		})
	},
}

var lineageGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one lineage",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		l, err := ws.Lineage(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(l, func() { fields("ID", l.Id, "Created", when(l.Created)) })
	},
}

// Hidden for the beta: the feature has nothing working behind it yet. The record of what is hidden and why is nest pkg/zerops/orchestrator/zbox/HIDDEN.md.
var lineageSetCmd = &cobra.Command{
	Use:    "set <id> --export on|off",
	Short:  "Turn lineage export on or off",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		var enabled bool
		switch flagLineageExport {
		case "on", "true":
			enabled = true
		case "off", "false":
		default:
			return usagef("--export takes on or off")
		}
		l, err := ws.UpdateLineage(cmd.Context(), args[0], enabled)
		if err != nil {
			return err
		}
		return emit(l, func() { result(l.Id, "Lineage %s export %s.", l.Id, onOff(l.ExportEnabled)) })
	},
}

// Hidden for the beta: the feature has nothing working behind it yet. The record of what is hidden and why is nest pkg/zerops/orchestrator/zbox/HIDDEN.md.
var lineageExportCmd = &cobra.Command{
	Use:    "export <id>",
	Short:  "Export a lineage to project object storage, full or incremental since the last export",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		e, err := ws.ExportLineage(cmd.Context(), args[0], flagExportDest)
		if err != nil {
			return err
		}
		return emit(e, func() { printExport(e) })
	},
}

// Hidden for the beta: the feature has nothing working behind it yet. The record of what is hidden and why is nest pkg/zerops/orchestrator/zbox/HIDDEN.md.
var exportCmd = &cobra.Command{
	Use:    "export",
	Short:  "Inspect lineage exports",
	Hidden: true,
}

var exportLsCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List exports",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Exports(cmd.Context(), flagExportLineage)
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "LINEAGE", "KIND", "STATUS", "SIZE", "DESTINATION", "CREATED")
			for _, e := range list {
				tb.row(e.Id, e.LineageId, e.Kind, e.Status, bytesPtr(e.SizeBytes), e.Destination, when(e.Created))
			}
			tb.ifEmpty("No exports.")
			tb.print()
		})
	},
}

var exportGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one export",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		e, err := ws.Export(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(e, func() { printExport(e) })
	},
}

func printExport(e *api.BaseExport) {
	pairs := []string{"ID", e.Id, "Lineage", e.LineageId, "Kind", string(e.Kind), "Status", string(e.Status), "Snapshot", e.SnapshotName, "Destination", e.Destination, "Size", bytesPtr(e.SizeBytes), "Created", when(e.Created), "Finished", whenPtr(e.Finished)}
	if e.SinceExportId != nil {
		pairs = append(pairs, "Since", *e.SinceExportId)
	}
	if e.Error != nil {
		pairs = append(pairs, "Error", *e.Error)
	}
	fields(pairs...)
}

// Hidden for the beta: the feature has nothing working behind it yet. The record of what is hidden and why is nest pkg/zerops/orchestrator/zbox/HIDDEN.md.
var importCmd = &cobra.Command{
	Use:    "import",
	Short:  "Import an exported lineage into this workspace",
	Long:   "Import an export of a lineage by its object storage key.",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagImportSource == "" {
			return usagef("--source is required")
		}
		ws, err := workspace()
		if err != nil {
			return err
		}
		return runOperation(cmd, ws, "imported", func() (*api.BaseOperation, error) {
			return ws.Import(cmd.Context(), api.RequestPostImport{Source: flagImportSource})
		})
	},
}

func init() {
	snapshotCreateCmd.Flags().BoolVar(&flagSnapMemory, "with-memory", false, "include the memory image, so a restore can resume the processes")
	waits(snapshotCreateCmd, "return the operation without waiting for it")
	snapshotRestoreCmd.Flags().StringVar(&flagRestoreName, "name", "", "name of the new sandbox")
	snapshotRestoreCmd.Flags().BoolVar(&flagRestoreMemory, "with-memory", false, "resume the memory image when the snapshot has one")
	waits(snapshotRestoreCmd, "return the operation without waiting for it")
	snapshotCmd.AddCommand(snapshotCreateCmd, snapshotLsCmd, snapshotGetCmd, snapshotRmCmd, snapshotRestoreCmd)

	lineageSetCmd.Flags().StringVar(&flagLineageExport, "export", "", "on or off")
	lineageExportCmd.Flags().StringVar(&flagExportDest, "destination", "", "object storage key prefix, defaults to the workspace setting")
	lineageCmd.AddCommand(lineageLsCmd, lineageGetCmd, lineageSetCmd, lineageExportCmd)

	exportLsCmd.Flags().StringVar(&flagExportLineage, "lineage", "", "only exports of this lineage")
	exportCmd.AddCommand(exportLsCmd, exportGetCmd)

	importCmd.Flags().StringVar(&flagImportSource, "source", "", "object storage key of the export")
	waits(importCmd, "return the operation without waiting for it")
	destructive(snapshotRmCmd)
	rootCmd.AddCommand(snapshotCmd, lineageCmd, exportCmd, importCmd)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
