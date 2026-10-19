package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagExecDir      string
	flagExecEnv      []string
	flagExecTimeout  time.Duration
	flagSessionKind  string
	flagSessionState string
	flagFileOut      string
	flagFileMode     string
)

func execRequest(argv []string) (api.RequestPostExec, error) {
	req := api.RequestPostExec{Command: argv, WorkingDir: optional(flagExecDir), TimeoutSeconds: seconds(flagExecTimeout)}
	if len(flagExecEnv) > 0 {
		env, err := parseEnv(flagExecEnv)
		if err != nil {
			return req, err
		}
		req.Env = &env
	}
	return req, nil
}

var execCmd = &cobra.Command{
	Use:   "exec <id|name> -- <command...>",
	Short: "Run a command in a sandbox and exit with its exit code",
	Long: `The command is argv, run without a shell: ` + "`zbox exec <id> -- sh -c 'a | b'`" + ` for a pipeline. A parked sandbox is woken first.
Stdout and stderr are the command's own. zbox exits with the command's exit code, with 125 when zbox itself failed
(the sandbox is not found, the workspace does not answer) and with 5 when --timeout ended the command.`,
	Example: `  zbox exec app -- python3 -c 'print(1+1)'
  zbox exec app -w /workspace -e DEBUG=1 -- npm test
  zbox exec app --timeout 30s -- sh -c 'curl -s localhost:3000 | head'`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		req, err := execRequest(args[1:])
		if err != nil {
			return err
		}
		res, err := ws.Exec(cmd.Context(), args[0], req)
		if err != nil {
			return err
		}
		if flagJson {
			return printJson(res)
		}
		fmt.Fprint(os.Stdout, res.Stdout)
		fmt.Fprint(os.Stderr, res.Stderr)
		if res.TimedOut {
			after := ""
			if flagExecTimeout > 0 {
				after = " after " + durationFlag(flagExecTimeout)
			}
			return waitTimeout{fmt.Sprintf("the command was stopped%s, its output is in `zbox session output %s`", after, res.SessionId)}
		}
		if res.ExitCode != nil && *res.ExitCode != 0 {
			return exitError{code: *res.ExitCode}
		}
		return nil
	},
}

var processCmd = &cobra.Command{
	Use:   "process <id|name> -- <command...>",
	Short: "Start a detached process in a sandbox and print its session",
	Long:  "The session id is the handle: `zbox session output` reads what it wrote, `zbox session interrupt` stops it.",
	Example: `  zbox process app -- python3 -m http.server 8000
  id=$(zbox process app -q -- ./server) && zbox session output $id`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		req, err := execRequest(args[1:])
		if err != nil {
			return err
		}
		s, err := ws.StartProcess(cmd.Context(), args[0], req)
		if err != nil {
			return err
		}
		return emit(s, func() { printSession(s) })
	},
}

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Guest sessions: detached processes and shells",
}

var sessionLsCmd = &cobra.Command{
	Use:     "list <sandbox-id|name>",
	Aliases: []string{"ls"},
	Short:   "List the sessions of a sandbox",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		p := api.GetSandboxSessionListParams{Kind: upperPtr[api.GetSandboxSessionListParamsKind](flagSessionKind), Status: upperPtr[api.GetSandboxSessionListParamsStatus](flagSessionState)}
		list, err := ws.Sessions(cmd.Context(), args[0], p)
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "KIND", "STATUS", "EXIT", "OUTPUT", "COMMAND", "CREATED", "CLOSED")
			for _, s := range list {
				tb.row(s.Id, s.Kind, s.Status, intPtr(s.ExitCode), yesNo(s.HasOutput), dash(commandString(s.Command)), when(s.Created), whenPtr(s.Closed))
			}
			tb.ifEmpty("No sessions.")
			tb.print()
		})
	},
}

var sessionGetCmd = &cobra.Command{
	Use:   "get <session-id>",
	Short: "Show one session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		s, err := ws.Session(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(s, func() { printSession(s) })
	},
}

var sessionInterruptCmd = &cobra.Command{
	Use:   "interrupt <session-id>",
	Short: "Interrupt a running session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		if err := ws.Interrupt(cmd.Context(), args[0]); err != nil {
			return err
		}
		result(args[0], "%s interrupted", args[0])
		return nil
	},
}

var sessionOutputCmd = &cobra.Command{
	Use:   "output <session-id>",
	Short: "Print the recorded output of a session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		r, err := ws.SessionOutput(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(os.Stdout, r)
		return err
	},
}

func printSession(s *api.BaseGuestSession) {
	fields("ID", s.Id, "Sandbox", s.SandboxId, "Kind", string(s.Kind), "Status", string(s.Status), "Exit code", intPtr(s.ExitCode), "Output", yesNo(s.HasOutput), "Command", dash(commandString(s.Command)), "Created", when(s.Created), "Closed", whenPtr(s.Closed))
}

func commandString(c *api.BaseExecCommand) string {
	if c == nil {
		return ""
	}
	return strings.Join(c.Command, " ")
}

func intPtr(p *int) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprint(*p)
}

var fileCmd = &cobra.Command{
	Use:   "file",
	Short: "Files inside a running sandbox",
}

var fileLsCmd = &cobra.Command{
	Use:     "list <id|name> <path>",
	Aliases: []string{"ls"},
	Short:   "List a directory",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Files(cmd.Context(), args[0], args[1])
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("NAME", "MODE", "SIZE", "MODIFIED")
			for _, f := range list {
				name := f.Name
				switch f.Type {
				case "DIRECTORY":
					name += "/"
				case "SYMLINK":
					name += " -> " + str(f.Target)
				}
				tb.row(name, f.Mode, bytesString(f.Size), when(f.Modified))
			}
			tb.ifEmpty("Empty directory.")
			tb.print()
		})
	},
}

var fileGetCmd = &cobra.Command{
	Use:     "get <id|name> <path>",
	Short:   "Download a file, to stdout or to -o",
	Example: `  zbox file get app /workspace/out.txt -o out.txt`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		r, err := ws.ReadFile(cmd.Context(), args[0], args[1])
		if err != nil {
			return err
		}
		defer r.Close()
		return writeOut(r, flagFileOut)
	},
}

var fileArchiveCmd = &cobra.Command{
	Use:   "archive <id|name> <path>",
	Short: "Download a directory as a tar stream, to stdout or to -o",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		r, err := ws.ReadArchive(cmd.Context(), args[0], args[1])
		if err != nil {
			return err
		}
		defer r.Close()
		return writeOut(r, flagFileOut)
	},
}

var filePutCmd = &cobra.Command{
	Use:   "put <id|name> <local-file> <path>",
	Short: "Upload a file, - reads stdin",
	Example: `  zbox file put app ./main.py /workspace/
  echo hi | zbox file put app - /workspace/hi.txt`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		var r io.Reader = os.Stdin
		if args[1] != "-" {
			f, err := os.Open(args[1])
			if err != nil {
				return err
			}
			defer f.Close()
			r = f
		}
		remote := args[2]
		if strings.HasSuffix(remote, "/") {
			remote = filepath.ToSlash(filepath.Join(remote, filepath.Base(args[1])))
		}
		if err := ws.WriteFile(cmd.Context(), args[0], remote, flagFileMode, r); err != nil {
			return err
		}
		result(remote, "%s uploaded", remote)
		return nil
	},
}

var fileRmCmd = &cobra.Command{
	Use:     "remove <id|name> <path>...",
	Aliases: []string{"rm"},
	Short:   "Delete files",
	Args:    cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		paths := args[1:]
		return deleteEach(fmt.Sprintf("delete %s in sandbox %s", strings.Join(paths, ", "), args[0]), "", paths, "deleted", func(i int) (*api.BaseOperation, error) {
			return nil, ws.DeleteFile(cmd.Context(), args[0], paths[i])
		})
	},
}

func writeOut(r io.Reader, path string) error {
	if path == "" || path == "-" {
		_, err := io.Copy(os.Stdout, r)
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Hidden for the beta: the feature has nothing working behind it yet. The record of what is hidden and why is nest pkg/zerops/orchestrator/zbox/HIDDEN.md.
var sshKeyCmd = &cobra.Command{
	Use:    "ssh-key <id|name> [public-key-file]",
	Short:  "Install an OpenSSH public key for an image's own sshd, defaults to ~/.ssh/id_ed25519.pub",
	Hidden: true,
	Args:   cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		path := ""
		if len(args) == 2 {
			path = args[1]
		} else {
			home, _ := os.UserHomeDir()
			for _, name := range []string{"id_ed25519.pub", "id_rsa.pub", "id_ecdsa.pub"} {
				if _, err := os.Stat(filepath.Join(home, ".ssh", name)); err == nil {
					path = filepath.Join(home, ".ssh", name)
					break
				}
			}
			if path == "" {
				return usagef("no public key in ~/.ssh, give the file")
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		key := strings.TrimSpace(string(data))
		if err := ws.AddSshKey(cmd.Context(), args[0], key); err != nil {
			return err
		}
		result(args[0], "Installed %s on %s.", path, args[0])
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{execCmd, processCmd} {
		c.Flags().StringVarP(&flagExecDir, "workdir", "w", "", "working directory inside the sandbox")
		c.Flags().StringArrayVarP(&flagExecEnv, "env", "e", nil, "environment variable as KEY=value, repeatable")
		c.Flags().DurationVar(&flagExecTimeout, "timeout", 0, "stop the command after this long, for example 30s")
	}
	sessionLsCmd.Flags().StringVar(&flagSessionKind, "kind", "", "pty, exec or ssh")
	sessionLsCmd.Flags().StringVar(&flagSessionState, "status", "", "open, closed or failed")
	sessionCmd.AddCommand(sessionLsCmd, sessionGetCmd, sessionInterruptCmd, sessionOutputCmd)

	fileGetCmd.Flags().StringVarP(&flagFileOut, "output", "o", "", "write to this local file instead of stdout")
	fileArchiveCmd.Flags().StringVarP(&flagFileOut, "output", "o", "", "write to this local file instead of stdout")
	filePutCmd.Flags().StringVar(&flagFileMode, "mode", "", "octal file mode, defaults to 0644")
	destructive(fileRmCmd)
	fileCmd.AddCommand(fileLsCmd, fileGetCmd, fileArchiveCmd, filePutCmd, fileRmCmd)

	rootCmd.AddCommand(execCmd, processCmd, sessionCmd, fileCmd, sshCmd, sshProxyCmd, sshConfigCmd, sshKeyCmd)
}
