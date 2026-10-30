package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagSnapMemory    bool
	flagRestoreName   string
	flagRestoreMemory bool
	flagRestoreSize   sizeFlags
	flagLineageExport string
	flagLineageLimit  int
	flagLineageBefore string
	flagExportLineage string
	flagExportSnap    string
	flagExportPrefix  string
	flagExportNoMem   bool
	flagImportName    string
	flagImportNoMem   bool
	flagImportExport  string
	flagImportFile    string
	flagExportOut     string
	flagS3            s3Flags
)

// s3Flags is the bucket of an export or import. The keys are names of Workspace environment variables, the CLI never sees their values.
type s3Flags struct {
	url, bucket, accessKeyEnv, secretKeyEnv string
}

// target is nil when no S3 flag is given, which makes an export a file on the workspace.
func (f s3Flags) target() (*api.BaseS3Target, error) {
	if f.url == "" && f.bucket == "" && f.accessKeyEnv == "" && f.secretKeyEnv == "" {
		return nil, nil
	}
	if f.url == "" || f.bucket == "" || f.accessKeyEnv == "" || f.secretKeyEnv == "" {
		return nil, usagef("--url, --bucket, --access-key-env and --secret-key-env go together")
	}
	return &api.BaseS3Target{Url: f.url, Bucket: f.bucket, AccessKeyIdEnv: f.accessKeyEnv, SecretAccessKeyEnv: f.secretKeyEnv}, nil
}

func s3FlagsOn(c *cobra.Command) {
	c.Flags().StringVar(&flagS3.url, "url", "", "endpoint of the S3 API, for example https://storage-prg1.zerops.io")
	c.Flags().StringVar(&flagS3.bucket, "bucket", "", "bucket name")
	c.Flags().StringVar(&flagS3.accessKeyEnv, "access-key-env", "", "workspace env variable holding the access key id")
	c.Flags().StringVar(&flagS3.secretKeyEnv, "secret-key-env", "", "workspace env variable holding the secret access key")
}

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
	Use:   "restore <snapshot-id>",
	Short: "Restore a snapshot into a new sandbox, the original is never rolled back",
	Long: `Restores a snapshot into a new sandbox at the snapshot's size. A restore without --with-memory boots cold and takes any --cpus,
--memory and --disk, a disk shrinks only down to what its files hold. A restore with memory keeps the snapshot's size.`,
	Example: `  zbox snapshot restore $(zbox snapshot list app -q | head -1) --name app-2 --with-memory
  zbox snapshot restore <snapshot-id> --name app-big --memory 4g --disk 40g`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPostSnapshotRestore{Name: optional(flagRestoreName)}
		if flagRestoreMemory {
			req.WithMemory = &flagRestoreMemory
		}
		if req.Cpus, req.MemoryBytes, req.DiskBytes, err = flagRestoreSize.values(); err != nil {
			return err
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
	Short:   "List lineages, newest first",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := listLineages(cmd, ws, api.GetLineageListParams{BeforeId: optional(flagLineageBefore)})
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
			if flagLineageLimit > 0 && len(list) == flagLineageLimit {
				morePages(list[len(list)-1].Id)
			}
		})
	},
}

// lineagePageMax is the largest page GET /lineage-list answers.
const lineagePageMax = 1000

// listLineages reads every lineage with no --limit, otherwise as many as asked for, paging back with beforeId past one page.
func listLineages(cmd *cobra.Command, ws *api.Workspace, p api.GetLineageListParams) ([]api.BaseLineage, error) {
	if flagLineageLimit <= 0 {
		return ws.Lineages(cmd.Context(), p)
	}
	var list []api.BaseLineage
	for len(list) < flagLineageLimit {
		page := min(lineagePageMax, flagLineageLimit-len(list))
		p.Limit = &page
		rows, err := ws.Lineages(cmd.Context(), p)
		if err != nil {
			return nil, err
		}
		list = append(list, rows...)
		if len(rows) < page {
			break
		}
		p.BeforeId = &rows[len(rows)-1].Id
	}
	return list, nil
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

// Hidden for the beta: the feature has nothing working behind it yet.
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

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export a parked sandbox or a snapshot to a file on the workspace or your own bucket, and inspect the exports",
}

var exportCreateCmd = &cobra.Command{
	Use:   "create [<sandbox-id|name>] [--snapshot <id>]",
	Short: "Write a parked sandbox's or a snapshot's disk, and its memory image when there is one, as one archive",
	Long: "Without the bucket flags the archive is a file on the workspace, downloaded with `zbox export download` (or --out right away) and " +
		"removed with its export a day after it finished. With them it goes to <prefix><lineageId>/<sandboxId>/<exportId>.tar.zst in the bucket, " +
		"whose keys are names of workspace env variables, never values. A running sandbox exports through a snapshot, a sandbox that received " +
		"secrets does not export at all.",
	Example: `  zbox export create app -o app.tar.zst
  zbox export create app --url https://storage-prg1.zerops.io --bucket abc12-backups --access-key-env storage_accessKeyId --secret-key-env storage_secretAccessKey`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if (len(args) == 1) == (flagExportSnap != "") {
			return usagef("give a parked sandbox or --snapshot, one of them")
		}
		to, err := flagS3.target()
		if err != nil {
			return err
		}
		if flagExportOut != "" && (to != nil || flagNoWait) {
			return usagef("--out downloads a file export when it finished, it takes neither the bucket flags nor --no-wait")
		}
		if flagExportPrefix != "" && to == nil {
			return usagef("--prefix is a key prefix in a bucket, it needs the bucket flags")
		}
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPostExport{Target: to, Prefix: optional(flagExportPrefix), SnapshotId: optional(flagExportSnap)}
		if len(args) == 1 {
			id, err := resolveSandbox(cmd.Context(), ws, args[0])
			if err != nil {
				return err
			}
			req.SandboxId = &id
		}
		if flagExportNoMem {
			withMemory := false
			req.WithMemory = &withMemory
		}
		op, err := ws.CreateExport(cmd.Context(), req)
		if err != nil {
			return err
		}
		if op, err = waitFor(cmd, ws, op); err != nil {
			return err
		}
		exportId, _ := resultString(op, "exportId")
		if flagExportOut != "" && exportId != "" {
			if err := downloadExport(cmd, ws, exportId, flagExportOut); err != nil {
				return err
			}
		}
		return emit(op, func() {
			if op.Result == nil {
				result(op.Id, "export started (operation %s)", op.Id)
				return
			}
			destination, _ := resultString(op, "destination")
			storage, _ := resultString(op, "storage")
			size, _ := (*op.Result)["sizeBytes"].(float64)
			memory, _ := (*op.Result)["hasMemory"].(bool)
			if storage != string(api.BaseExportStorageFILE) {
				result(destination, "exported to %s, %s, memory %s%s (operation %s)", destination, bytesString(int64(size)), yesNo(memory), tookNote(op), op.Id)
				return
			}
			result(exportId, "exported %s to a file on the workspace, %s, memory %s%s (operation %s), download route %s", exportId, bytesString(int64(size)), yesNo(memory), tookNote(op), op.Id, destination)
			if flagExportOut == "" {
				note("`zbox export download %s -o <file>` downloads it, the workspace keeps it for a day.", exportId)
			}
		})
	},
}

func resultString(op *api.BaseOperation, key string) (string, bool) {
	if op.Result == nil {
		return "", false
	}
	v, ok := (*op.Result)[key].(string)
	return v, ok
}

var exportDownloadCmd = &cobra.Command{
	Use:   "download <export-id> [-o <file>]",
	Short: "Download the archive of a file export",
	Long:  "Writes the archive to the file, `<sandboxId>-<exportId>.tar.zst` in the current directory without -o, `-o -` writes it to stdout.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		path := flagExportOut
		if path == "" {
			e, err := ws.Export(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			path = e.SandboxId + "-" + e.Id + ".tar.zst"
		}
		return downloadExport(cmd, ws, args[0], path)
	},
}

// downloadExport streams the archive into the file through a sibling .part file, renamed when the whole archive arrived.
func downloadExport(cmd *cobra.Command, ws *api.Workspace, id, path string) error {
	body, size, err := ws.ExportArchive(cmd.Context(), id)
	if err != nil {
		return err
	}
	defer body.Close()
	if path == "-" {
		_, err := io.Copy(os.Stdout, body)
		return err
	}
	part := path + ".part"
	file, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	progress := newProgress(size)
	_, err = io.Copy(io.MultiWriter(file, progress), body)
	progress.done()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil && size >= 0 && progress.written != size {
		err = fmt.Errorf("the download ended after %s of %s", bytesString(progress.written), bytesString(size))
	}
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	if err := os.Rename(part, path); err != nil {
		return err
	}
	note("Downloaded %s to %s.", bytesString(progress.written), path)
	return nil
}

var stderrTerminal = term.IsTerminal(int(os.Stderr.Fd()))

// progress prints the bytes written so far to stderr at most every half a second, on a terminal only.
type progress struct {
	total, written int64
	last           time.Time
	shown          bool
}

func newProgress(total int64) *progress {
	return &progress{total: total}
}

func (p *progress) Write(b []byte) (int, error) {
	p.written += int64(len(b))
	if stderrTerminal && !flagQuiet && !flagJson && time.Since(p.last) >= 500*time.Millisecond {
		p.last = time.Now()
		p.shown = true
		if p.total > 0 {
			fmt.Fprintf(os.Stderr, "\r%s of %s (%d %%)  ", bytesString(p.written), bytesString(p.total), p.written*100/p.total)
		} else {
			fmt.Fprintf(os.Stderr, "\r%s  ", bytesString(p.written))
		}
	}
	return len(b), nil
}

func (p *progress) done() {
	if p.shown {
		fmt.Fprintln(os.Stderr)
	}
}

var exportRmCmd = &cobra.Command{
	Use:     "remove <export-id>...",
	Aliases: []string{"rm"},
	Short:   "Delete exports: the row, and the file of a file export",
	Long:    "The object of a bucket export stays in its bucket, the workspace never deletes from it. A running export is refused.",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		return deleteEach("delete export "+strings.Join(args, ", "), "", args, "deleted", func(i int) (*api.BaseOperation, error) {
			return nil, ws.DeleteExport(cmd.Context(), args[i])
		})
	},
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
			tb := newTable("ID", "SANDBOX", "SNAPSHOT", "MEMORY", "STATUS", "STORAGE", "SIZE", "DESTINATION", "CREATED")
			for _, e := range list {
				tb.row(e.Id, e.SandboxId, e.SnapshotName, yesNo(e.HasMemory), e.Status, e.Storage, bytesPtr(e.SizeBytes), e.Destination, when(e.Created))
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
	pairs := []string{"ID", e.Id, "Sandbox", e.SandboxId, "Lineage", e.LineageId, "Status", string(e.Status), "Snapshot", e.SnapshotName, "Memory", yesNo(e.HasMemory), "Storage", string(e.Storage), "Destination", e.Destination, "Size", bytesPtr(e.SizeBytes), "Created", when(e.Created), "Finished", whenPtr(e.Finished)}
	if e.Error != nil {
		pairs = append(pairs, "Error", *e.Error)
	}
	fields(pairs...)
}

var importCmd = &cobra.Command{
	Use:   "import <key> | --export <export-id> | --file <archive>",
	Short: "Create a sandbox from an export's archive",
	Long: "The archive is a file export of this workspace (--export), a local file any workspace exported (--file, uploaded and kept as an " +
		"export for a day) or an object in a bucket: the key is the part of an export's destination after the bucket. The base it was made " +
		"from must be on this workspace. The memory image resumes only from an archive this workspace exported, on the same Firecracker " +
		"release, CPU model and base build, otherwise the disk boots cold.",
	Example: `  zbox import --export Xy7... --name app-copy
  zbox import --file app.tar.zst --name app-copy
  zbox import 9vbp.../a1b2c3d4/Xy7....tar.zst --name app-copy --url https://storage-prg1.zerops.io --bucket abc12-backups --access-key-env storage_accessKeyId --secret-key-env storage_secretAccessKey`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		from, err := flagS3.target()
		if err != nil {
			return err
		}
		if flagImportFile != "" {
			if flagImportExport != "" || len(args) > 0 || from != nil {
				return usagef("--file takes neither a key, the bucket flags nor --export")
			}
			return importFile(cmd, flagImportFile)
		}
		req := api.RequestPostImport{Name: optional(flagImportName)}
		switch {
		case flagImportExport != "" && len(args) == 0 && from == nil:
			req.ExportId = &flagImportExport
		case flagImportExport == "" && len(args) == 1 && from != nil:
			req.Source, req.Key = from, &args[0]
		default:
			return usagef("give --export <id>, or a key with --url, --bucket, --access-key-env and --secret-key-env")
		}
		ws, err := workspace()
		if err != nil {
			return err
		}
		if flagImportNoMem {
			withMemory := false
			req.WithMemory = &withMemory
		}
		return runSpawn(cmd, ws, func() (*api.ResponseSpawn, error) { return ws.Import(cmd.Context(), req) })
	},
}

// importFile uploads the archive with the progress on stderr, then follows the import like any spawn.
func importFile(cmd *cobra.Command, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	ws, err := workspace()
	if err != nil {
		return err
	}
	p := api.PostImportUploadParams{Name: optional(flagImportName)}
	if flagImportNoMem {
		withMemory := false
		p.WithMemory = &withMemory
	}
	return runSpawn(cmd, ws, func() (*api.ResponseSpawn, error) {
		progress := newProgress(info.Size())
		defer progress.done()
		return ws.ImportUpload(cmd.Context(), p, io.TeeReader(file, progress), info.Size())
	})
}

func init() {
	snapshotCreateCmd.Flags().BoolVar(&flagSnapMemory, "with-memory", false, "include the memory image, so a restore can resume the processes")
	waits(snapshotCreateCmd, "return the operation without waiting for it")
	snapshotRestoreCmd.Flags().StringVar(&flagRestoreName, "name", "", "name of the new sandbox")
	snapshotRestoreCmd.Flags().BoolVar(&flagRestoreMemory, "with-memory", false, "resume the memory image when the snapshot has one")
	flagRestoreSize.add(snapshotRestoreCmd, "the snapshot's")
	waits(snapshotRestoreCmd, "return the operation without waiting for it")
	snapshotCmd.AddCommand(snapshotCreateCmd, snapshotLsCmd, snapshotGetCmd, snapshotRmCmd, snapshotRestoreCmd)

	lineageSetCmd.Flags().StringVar(&flagLineageExport, "export", "", "on or off")
	lineageLsCmd.Flags().IntVar(&flagLineageLimit, "limit", 0, "at most this many, the newest first; every lineage when omitted")
	lineageLsCmd.Flags().StringVar(&flagLineageBefore, "before", "", "only lineages created before this one (the last id of the previous page)")
	lineageCmd.AddCommand(lineageLsCmd, lineageGetCmd, lineageSetCmd)

	exportCreateCmd.Flags().StringVar(&flagExportSnap, "snapshot", "", "export this snapshot instead of a parked sandbox")
	exportCreateCmd.Flags().StringVar(&flagExportPrefix, "prefix", "", "key prefix in the bucket, for example backups/")
	exportCreateCmd.Flags().BoolVar(&flagExportNoMem, "no-memory", false, "leave the memory image out, the disk alone")
	exportCreateCmd.Flags().StringVarP(&flagExportOut, "out", "o", "", "download the file export to this path when it finished")
	s3FlagsOn(exportCreateCmd)
	waits(exportCreateCmd, "return the operation without waiting for the archive")
	exportLsCmd.Flags().StringVar(&flagExportLineage, "lineage", "", "only exports of this lineage")
	exportDownloadCmd.Flags().StringVarP(&flagExportOut, "out", "o", "", "write the archive here, - for stdout")
	exportCmd.AddCommand(exportCreateCmd, exportLsCmd, exportGetCmd, exportDownloadCmd, exportRmCmd)

	importCmd.Flags().StringVar(&flagImportName, "name", "", "name of the new sandbox")
	importCmd.Flags().BoolVar(&flagImportNoMem, "no-memory", false, "boot the disk cold even when the archive has a memory image")
	importCmd.Flags().StringVar(&flagImportExport, "export", "", "import the file of this file export instead of an object in a bucket")
	importCmd.Flags().StringVar(&flagImportFile, "file", "", "upload this archive and import it")
	s3FlagsOn(importCmd)
	waits(importCmd, "return the operation without waiting for it")
	destructive(snapshotRmCmd, exportRmCmd)
	rootCmd.AddCommand(snapshotCmd, lineageCmd, exportCmd, importCmd)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
