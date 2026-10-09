package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

// Exit codes, the contract in docs/cli-ux.md. A caller branches on them instead of parsing messages.
const (
	exitFailed       = 1
	exitUsage        = 2
	exitNotFound     = 3
	exitConflict     = 4
	exitTimeout      = 5
	exitUnauthorized = 6
	exitUnavailable  = 7
	exitExecFailed   = 125
	exitInterrupted  = 130
)

// usageError is a command called wrong: a missing or contradictory flag, a malformed value, the wrong number of arguments.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

// notFoundError is a reference the CLI resolved itself (a base, a template by name) and found nothing for. The API's own not-found answers are ApiErrors.
type notFoundError struct{ err error }

func (e notFoundError) Error() string { return e.err.Error() }
func (e notFoundError) Unwrap() error { return e.err }

func notFoundf(format string, args ...any) error {
	return notFoundError{fmt.Errorf(format, args...)}
}

type interrupted struct{}

func (interrupted) Error() string { return "interrupted" }

// waitTimeout is a --timeout that ran out: the work goes on on the workspace (a wait given up, an exec stopped), the message says how to pick it up. Exit 5.
type waitTimeout struct{ msg string }

func (e waitTimeout) Error() string { return e.msg }

// durationFlag echoes a duration flag the way it was given, 30s or 5m, not Go's 5m0s. Measured durations go through durationString.
func durationFlag(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// markUsage makes the argument and flag errors of every command usage errors, so they exit 2 like the ones the commands raise themselves.
func markUsage(c *cobra.Command) {
	c.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	if args := c.Args; args != nil {
		c.Args = func(cmd *cobra.Command, a []string) error {
			if err := args(cmd, a); err != nil {
				return usageError{fmt.Errorf("%w, see `%s --help`", err, cmd.CommandPath())}
			}
			return nil
		}
	}
	for _, sub := range c.Commands() {
		markUsage(sub)
	}
}

func exitCode(err error) int {
	var apiErr api.ApiError
	var failed api.OperationFailed
	switch {
	case errors.As(err, new(usageError)), isCobraUsage(err):
		return exitUsage
	case errors.As(err, new(interrupted)), errors.Is(err, context.Canceled):
		return exitInterrupted
	case errors.As(err, new(waitTimeout)), errors.Is(err, context.DeadlineExceeded):
		return exitTimeout
	case errors.As(err, &failed):
		return exitFailed
	case errors.As(err, new(notFoundError)):
		return exitNotFound
	case errors.As(err, new(notAuthorized)):
		return exitUnauthorized
	case errors.As(err, &apiErr):
		switch {
		case apiErr.Status == 404 || strings.HasSuffix(apiErr.Code, "NotFound"):
			return exitNotFound
		case apiErr.Status == 400 || apiErr.Status == 422:
			return exitUsage
		case apiErr.Status == 401 || apiErr.Status == 403:
			return exitUnauthorized
		case apiErr.Status == 409:
			return exitConflict
		case apiErr.Status == 504:
			return exitTimeout
		case apiErr.Status == 502 || apiErr.Status == 503:
			return exitUnavailable
		}
		return exitFailed
	case errors.As(err, new(net.Error)):
		return exitUnavailable
	}
	return exitFailed
}

// isCobraUsage catches the errors cobra raises before any hook of ours runs, an unknown command or flag.
func isCobraUsage(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "unknown command") || strings.HasPrefix(msg, "unknown flag") || strings.HasPrefix(msg, "unknown shorthand flag") || strings.HasPrefix(msg, "required flag")
}

// notAuthorized is a missing login or workspace key on this machine, before anything was asked of an API.
type notAuthorized struct{ msg string }

func (e notAuthorized) Error() string { return e.msg }

// reportError prints the error to stderr: one JSON object under --json, `error: …` otherwise.
func reportError(err error, code int) {
	if !flagJson {
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}
	out := map[string]any{"message": err.Error(), "exitCode": code}
	var e api.ApiError
	var failed api.OperationFailed
	switch {
	case errors.As(err, &failed):
		out["code"] = "operationFailed"
		out["operation"] = failed.Op
	case errors.As(err, &e):
		out["code"], out["status"], out["message"] = e.Code, e.Status, e.Message
		if len(e.Meta) > 0 {
			out["meta"] = e.Meta
		}
	default:
		out["code"] = map[int]string{exitUsage: "usage", exitNotFound: "notFound", exitTimeout: "timeout", exitUnauthorized: "unauthorized", exitUnavailable: "unavailable", exitInterrupted: "interrupted"}[code]
		if out["code"] == "" {
			out["code"] = "failed"
		}
	}
	data, _ := json.Marshal(map[string]any{"error": out})
	fmt.Fprintln(os.Stderr, string(data))
}

// confirm asks before a destructive change: action is what happens ("terminate sandbox x"), warning what it costs. --yes answers it; without a terminal to ask on, the command refuses rather than guess, so an unattended run never destroys by default.
func confirm(action, warning string) error {
	if flagYes {
		return nil
	}
	if !stdinTerminal {
		return usagef("pass --yes to %s, stdin is not a terminal to ask on", action)
	}
	question := strings.ToUpper(action[:1]) + action[1:] + "?"
	if warning != "" {
		question += " " + warning
	}
	return ask(question)
}

// ask puts a yes/no question on stderr and fails with "cancelled" on anything but a yes. The caller has checked there is a terminal.
func ask(question string) error {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		return errors.New("cancelled")
	}
	return nil
}

var flagYes bool

// destructive registers --yes on the commands that delete or drop something.
func destructive(cmds ...*cobra.Command) {
	for _, c := range cmds {
		c.Flags().BoolVarP(&flagYes, "yes", "y", false, "do not ask for confirmation")
	}
}

// deleteEach is the shape of every rm: one confirmation naming the targets (none when action is empty), then one call and one result line per target, `label deleted` or `label terminated in 70 ms (operation x)` when the call answers an operation. It stops at the first failure. Under --json the lines are one array of {id, operation}.
func deleteEach(action, warning string, labels []string, verbPast string, del func(i int) (*api.BaseOperation, error)) error {
	if action != "" {
		if err := confirm(action, warning); err != nil {
			return err
		}
	}
	var done []map[string]any
	for i, label := range labels {
		op, err := del(i)
		if err != nil {
			return err
		}
		item := map[string]any{"id": label}
		line := verbPast
		if op != nil {
			item["operation"] = op
			line = fmt.Sprintf("%s%s (operation %s)", verb(op, verbPast), tookNote(op), op.Id)
		}
		done = append(done, item)
		if !flagJson {
			result(label, "%s %s", label, line)
		}
	}
	if flagJson {
		return printJson(done)
	}
	return nil
}

var flagWaitTimeout time.Duration

// waits registers --no-wait and --timeout on a command that waits for its operation by default.
func waits(c *cobra.Command, noWait string) {
	c.Flags().BoolVar(&flagNoWait, "no-wait", false, noWait)
	timeoutFlag(c)
}

// timeoutFlag registers --timeout alone, for a command that only waits.
func timeoutFlag(c *cobra.Command) {
	c.Flags().DurationVar(&flagWaitTimeout, "timeout", 0, "give up waiting after this long, for example 5m (exit 5); the work goes on on the workspace")
}

// waitContext bounds a wait by --timeout. The returned error turns a deadline into a waitTimeout naming what still runs.
func waitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if flagWaitTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, flagWaitTimeout)
}

// timedOut turns the deadline of a wait into the error that says what is still running and how to wait for it again.
func timedOut(err error, what, next string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return waitTimeout{fmt.Sprintf("gave up after %s, %s, %s", durationFlag(flagWaitTimeout), what, next)}
	}
	return err
}

// exitError carries a remote exit code (zbox exec) to the process exit status without printing anything.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }
