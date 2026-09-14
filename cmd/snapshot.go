package cmd

import (
	"errors"
	"fmt"
	"io"

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
	flagImportKeyEnv  string
)

var snapshotCmd = &cobra.Command{
	Use:     "snapshot",
	Aliases: []string{"snap"},
	Short:   "Take, list and restore snapshots",
}

var snapshotCreateCmd = &cobra.Command{
	Use:   "create <sandbox-id> <name>",
	Short: "Take a named snapshot of a sandbox's disk, optionally with its memory",
	Args:  cobra.ExactArgs(2),
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
	Use:   "ls <sandbox-id>",
	Short: "List the snapshots of a sandbox",
	Args:  cobra.ExactArgs(1),
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
			table("ID\tNAME\tKIND\tMEMORY\tSIZE\tCREATED", func(w io.Writer) {
				for _, s := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\t%s\n", s.Id, dash(str(s.Name)), s.Kind, s.HasMemory, bytesPtr(s.SizeBytes), when(s.Created))
				}
			})
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
			fields("ID", s.Id, "Name", dash(str(s.Name)), "Sandbox", s.SandboxId, "Kind", string(s.Kind), "Memory", fmt.Sprint(s.HasMemory), "Size", bytesPtr(s.SizeBytes), "Created", when(s.Created))
		})
	},
}

var snapshotRmCmd = &cobra.Command{
	Use:   "rm <snapshot-id>...",
	Short: "Delete snapshots",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		for _, id := range args {
			if err := ws.DeleteSnapshot(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Println("deleted", id)
		}
		return nil
	},
}

var snapshotRestoreCmd = &cobra.Command{
	Use:   "restore <snapshot-id>",
	Short: "Restore a snapshot into a new sandbox, the original is never rolled back",
	Args:  cobra.ExactArgs(1),
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
	Use:   "ls",
	Short: "List lineages",
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
			table("ID\tEXPORT\tCREATED", func(w io.Writer) {
				for _, l := range list {
					fmt.Fprintf(w, "%s\t%v\t%s\n", l.Id, l.ExportEnabled, when(l.Created))
				}
			})
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
		return emit(l, func() { fields("ID", l.Id, "Export", fmt.Sprint(l.ExportEnabled), "Created", when(l.Created)) })
	},
}

var lineageSetCmd = &cobra.Command{
	Use:   "set <id> --export on|off",
	Short: "Turn lineage export on or off",
	Args:  cobra.ExactArgs(1),
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
			return errors.New("--export takes on or off")
		}
		l, err := ws.UpdateLineage(cmd.Context(), args[0], enabled)
		if err != nil {
			return err
		}
		return emit(l, func() { fmt.Printf("Lineage %s export %v.\n", l.Id, l.ExportEnabled) })
	},
}

var lineageExportCmd = &cobra.Command{
	Use:   "export <id>",
	Short: "Export a lineage to project object storage, full or incremental since the last export",
	Args:  cobra.ExactArgs(1),
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

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Inspect lineage exports",
}

var exportLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List exports",
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
			table("ID\tLINEAGE\tKIND\tSTATUS\tSIZE\tDESTINATION\tCREATED", func(w io.Writer) {
				for _, e := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Id, e.LineageId, e.Kind, e.Status, bytesPtr(e.SizeBytes), e.Destination, when(e.Created))
				}
			})
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

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Import an exported lineage into this workspace",
	Long:  "The export is useless without the source workspace's master key: name the env of this workspace that holds it.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagImportSource == "" || flagImportKeyEnv == "" {
			return errors.New("--source and --master-key-env are required")
		}
		ws, err := workspace()
		if err != nil {
			return err
		}
		return runOperation(cmd, ws, "imported", func() (*api.BaseOperation, error) {
			return ws.Import(cmd.Context(), api.RequestPostImport{Source: flagImportSource, MasterKeyEnv: flagImportKeyEnv})
		})
	},
}

func init() {
	snapshotCreateCmd.Flags().BoolVar(&flagSnapMemory, "with-memory", false, "include the memory image, so a restore can resume the processes")
	snapshotCreateCmd.Flags().BoolVar(&flagNoWait, "no-wait", false, "return the operation without waiting for it")
	snapshotRestoreCmd.Flags().StringVar(&flagRestoreName, "name", "", "name of the new sandbox")
	snapshotRestoreCmd.Flags().BoolVar(&flagRestoreMemory, "with-memory", false, "resume the memory image when the snapshot has one")
	snapshotRestoreCmd.Flags().BoolVar(&flagNoWait, "no-wait", false, "return the operation without waiting for it")
	snapshotCmd.AddCommand(snapshotCreateCmd, snapshotLsCmd, snapshotGetCmd, snapshotRmCmd, snapshotRestoreCmd)

	lineageSetCmd.Flags().StringVar(&flagLineageExport, "export", "", "on or off")
	lineageExportCmd.Flags().StringVar(&flagExportDest, "destination", "", "object storage key prefix, defaults to the workspace setting")
	lineageCmd.AddCommand(lineageLsCmd, lineageGetCmd, lineageSetCmd, lineageExportCmd)

	exportLsCmd.Flags().StringVar(&flagExportLineage, "lineage", "", "only exports of this lineage")
	exportCmd.AddCommand(exportLsCmd, exportGetCmd)

	importCmd.Flags().StringVar(&flagImportSource, "source", "", "object storage key of the export")
	importCmd.Flags().StringVar(&flagImportKeyEnv, "master-key-env", "", "env of this workspace holding the source workspace's master key")
	importCmd.Flags().BoolVar(&flagNoWait, "no-wait", false, "return the operation without waiting for it")
	rootCmd.AddCommand(snapshotCmd, lineageCmd, exportCmd, importCmd)
}
