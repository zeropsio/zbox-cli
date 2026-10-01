package cmd

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

// Sandbox ids are eight lowercase alphanumerics. Anything else can only be a name.
var sandboxIdPattern = regexp.MustCompile(`^[a-z0-9]{8}$`)

// resolveSandbox turns a sandbox argument into an id: an argument shaped like an id is tried as one first and wins when it exists, otherwise it is looked up as a name among the live sandboxes (a name is unique among them, the orchestrator refuses a duplicate at create; terminated and archived rows may reuse it). No match keeps the API's sandboxNotFound, several live matches (which cannot happen today) name the ids.
func resolveSandbox(ctx context.Context, ws *api.Workspace, arg string) (string, error) {
	var notFound error
	if sandboxIdPattern.MatchString(arg) {
		_, err := ws.Sandbox(ctx, arg)
		if err == nil {
			return arg, nil
		}
		if !api.IsCode(err, "sandboxNotFound") {
			return "", err
		}
		notFound = err
	}
	list, err := ws.Sandboxes(ctx, api.GetSandboxListParams{})
	if err != nil {
		return "", err
	}
	var ids []string
	for _, s := range list {
		if s.Status == api.BaseSandboxStatusTERMINATED || s.Status == api.BaseSandboxStatusARCHIVED {
			continue
		}
		if s.Name != nil && *s.Name == arg {
			ids = append(ids, s.Id)
		}
	}
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		if notFound != nil {
			return "", notFound
		}
		return "", &api.ApiError{Status: 404, Code: "sandboxNotFound", Message: fmt.Sprintf("no live sandbox named %q", arg)}
	default:
		sort.Strings(ids)
		return "", fmt.Errorf("%d live sandboxes are named %q, use the id: %s", len(ids), arg, strings.Join(ids, ", "))
	}
}

// resolveSandboxArgs is the PreRunE of every command whose leading arguments are sandboxes: it rewrites those arguments in place (cobra hands RunE the same slice), so the RunE bodies keep taking ids. count is how many leading arguments are sandboxes, or -1 for all of them (`rm`).
func resolveSandboxArgs(count int) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		n := count
		if n < 0 || n > len(args) {
			n = len(args)
		}
		for i := 0; i < n; i++ {
			id, err := resolveSandbox(cmd.Context(), ws, args[i])
			if err != nil {
				return err
			}
			args[i] = id
		}
		return nil
	}
}

// sandboxArgs marks the commands whose first argument is a sandbox; sandboxArgsAll the ones taking only sandboxes.
func sandboxArgs(cmds ...*cobra.Command) {
	for _, c := range cmds {
		c.PreRunE = resolveSandboxArgs(1)
	}
}

func sandboxArgsAll(cmds ...*cobra.Command) {
	for _, c := range cmds {
		c.PreRunE = resolveSandboxArgs(-1)
	}
}

func init() {
	sandboxArgs(getCmd, setCmd, parkCmd, wakeCmd, forkCmd, shellCmd, execCmd, processCmd, sessionLsCmd,
		fileLsCmd, fileGetCmd, fileArchiveCmd, filePutCmd, fileRmCmd, sshCmd, sshKeyCmd,
		policyCmd, portLsCmd, portOpenCmd, portCloseCmd, attachCmd, detachCmd, attachmentsCmd,
		credentialIssueCmd, credentialLsCmd, metricsCmd, snapshotCreateCmd, snapshotLsCmd, openCmd)
	sandboxArgsAll(rmCmd)
}
