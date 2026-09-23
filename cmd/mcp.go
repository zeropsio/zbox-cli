package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagMcpTemplate string
	flagMcpBase     string
	flagMcpNetwork  string
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the workspace as an MCP server on stdio, for Claude Code and other agents",
	Long: `Tools: run_code (a fresh sandbox per call unless one is kept or named) and kill_sandbox.
Register it with: claude mcp add zbox -- zbox mcp`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		s := mcp.NewServer(&mcp.Implementation{Name: "zbox", Version: "0.1.0"}, nil)
		r := &mcpRunner{cmd: cmd, ws: ws}
		mcp.AddTool(s, &mcp.Tool{
			Name:        "run_code",
			Description: "Run code in an isolated Firecracker sandbox VM. Without sandbox_id a fresh VM is created for the call and destroyed afterwards unless keep is true. The code's stdout is the result, so print what you need. Files under /workspace persist while the sandbox is kept.",
		}, r.runCode)
		mcp.AddTool(s, &mcp.Tool{
			Name:        "kill_sandbox",
			Description: "Destroy a sandbox kept by an earlier run_code call.",
		}, r.killSandbox)
		return s.Run(cmd.Context(), &mcp.StdioTransport{})
	},
}

type mcpRunner struct {
	cmd *cobra.Command
	ws  *api.Workspace
}

type runCodeIn struct {
	Code           string `json:"code" jsonschema:"the code to run"`
	Language       string `json:"language,omitempty" jsonschema:"python (default), javascript or bash"`
	SandboxId      string `json:"sandbox_id,omitempty" jsonschema:"run in this kept sandbox instead of a fresh one"`
	Keep           bool   `json:"keep,omitempty" jsonschema:"keep a freshly created sandbox for later calls, the answer carries its id"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"execution timeout, default 50, at most 50 (the API request has a 60 s limit)"`
}

type runCodeOut struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
	SandboxId string `json:"sandbox_id,omitempty"`
	CreateMs  int64  `json:"create_ms,omitempty"`
	RunMs     int64  `json:"run_ms"`
}

func (r *mcpRunner) runCode(ctx context.Context, _ *mcp.CallToolRequest, in runCodeIn) (*mcp.CallToolResult, runCodeOut, error) {
	var out runCodeOut
	argv, err := codeArgv(in.Language, in.Code)
	if err != nil {
		return nil, out, err
	}
	id := in.SandboxId
	fresh := id == ""
	if fresh {
		started := time.Now()
		id, err = r.create(ctx)
		if err != nil {
			return nil, out, err
		}
		out.CreateMs = time.Since(started).Milliseconds()
	}
	// The edge drops an API request after 60 s, so one exec must end before that.
	timeout := in.TimeoutSeconds
	if timeout <= 0 || timeout > 50 {
		timeout = 50
	}
	started := time.Now()
	res, err := r.ws.Exec(ctx, id, api.RequestPostExec{Command: argv, WorkingDir: optional("/workspace"), TimeoutSeconds: &timeout})
	out.RunMs = time.Since(started).Milliseconds()
	if fresh && !in.Keep {
		if _, killErr := r.ws.DeleteSandbox(context.WithoutCancel(ctx), id); killErr != nil && err == nil {
			err = fmt.Errorf("the code ran but the sandbox was not destroyed: %v", killErr)
		}
	} else {
		out.SandboxId = id
	}
	if err != nil {
		return nil, out, err
	}
	out.Stdout, out.Stderr, out.TimedOut = res.Stdout, res.Stderr, res.TimedOut
	if res.ExitCode != nil {
		out.ExitCode = *res.ExitCode
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out.text()}}}, out, nil
}

func (o runCodeOut) text() string {
	var b strings.Builder
	if o.Stdout != "" {
		b.WriteString(o.Stdout)
		if !strings.HasSuffix(o.Stdout, "\n") {
			b.WriteString("\n")
		}
	}
	if o.Stderr != "" {
		b.WriteString("stderr:\n" + o.Stderr)
		if !strings.HasSuffix(o.Stderr, "\n") {
			b.WriteString("\n")
		}
	}
	switch {
	case o.TimedOut:
		b.WriteString("timed out\n")
	case o.ExitCode != 0:
		fmt.Fprintf(&b, "exit code %d\n", o.ExitCode)
	}
	if o.CreateMs > 0 {
		fmt.Fprintf(&b, "[sandbox created in %d ms, code ran in %d ms", o.CreateMs, o.RunMs)
	} else {
		fmt.Fprintf(&b, "[code ran in %d ms", o.RunMs)
	}
	if o.SandboxId != "" {
		fmt.Fprintf(&b, ", sandbox %s kept", o.SandboxId)
	} else {
		b.WriteString(", sandbox destroyed")
	}
	b.WriteString("]\n")
	return b.String()
}

func codeArgv(language, code string) ([]string, error) {
	switch strings.ToLower(language) {
	case "", "python", "python3", "py":
		return []string{"python3", "-c", code}, nil
	case "javascript", "js", "node":
		return []string{"node", "-e", code}, nil
	case "bash", "sh", "shell":
		return []string{"bash", "-c", code}, nil
	}
	return nil, fmt.Errorf("unknown language %q, use python, javascript or bash", language)
}

// create spawns a sandbox from the configured template or base and waits for it to be running.
func (r *mcpRunner) create(ctx context.Context) (string, error) {
	req := api.RequestPostSandbox{NetworkMode: upper[api.RequestPostSandboxNetworkMode](flagMcpNetwork), Name: optional("mcp-" + time.Now().Format("150405"))}
	switch {
	case flagMcpTemplate != "":
		t, err := findTemplate(r.cmd, r.ws, flagMcpTemplate)
		if err != nil {
			return "", err
		}
		req.TemplateDigest = &t.Digest
	case flagMcpBase != "":
		b, err := findBase(r.cmd, r.ws, flagMcpBase)
		if err != nil {
			return "", err
		}
		req.BaseDigest = &b.Digest
	default:
		t, err := readyTemplate(ctx, r.ws)
		if err != nil {
			return "", err
		}
		if t != nil {
			req.TemplateDigest = &t.Digest
		} else {
			b, err := findBase(r.cmd, r.ws, "")
			if err != nil {
				return "", err
			}
			req.BaseDigest = &b.Digest
		}
	}
	spawn, err := r.ws.CreateSandbox(ctx, req)
	if err != nil {
		return "", err
	}
	if len(spawn.Sandboxes) != 1 {
		return "", errors.New("create answered no sandbox")
	}
	if _, err := r.ws.WaitOperation(ctx, spawn.Operation.Id); err != nil {
		return "", err
	}
	return spawn.Sandboxes[0].Id, nil
}

// readyTemplate picks the first READY template, nil when the workspace has none.
func readyTemplate(ctx context.Context, ws *api.Workspace) (*api.BaseTemplate, error) {
	templates, err := ws.Templates(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range templates {
		if t.Status == api.BaseTemplateStatusREADY {
			return &t, nil
		}
	}
	return nil, nil
}

type killSandboxIn struct {
	SandboxId string `json:"sandbox_id" jsonschema:"the sandbox to destroy"`
}

type killSandboxOut struct {
	SandboxId string `json:"sandbox_id"`
	Status    string `json:"status"`
}

func (r *mcpRunner) killSandbox(ctx context.Context, _ *mcp.CallToolRequest, in killSandboxIn) (*mcp.CallToolResult, killSandboxOut, error) {
	op, err := r.ws.DeleteSandbox(ctx, in.SandboxId)
	if err != nil {
		return nil, killSandboxOut{}, err
	}
	if _, err := r.ws.WaitOperation(ctx, op.Id); err != nil {
		return nil, killSandboxOut{}, err
	}
	out := killSandboxOut{SandboxId: in.SandboxId, Status: "destroyed"}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "sandbox " + in.SandboxId + " destroyed\n"}}}, out, nil
}

func init() {
	f := mcpCmd.Flags()
	f.StringVar(&flagMcpTemplate, "template", "", "template to spawn from, the first ready one when omitted")
	f.StringVar(&flagMcpBase, "base", "", "base to boot from instead of a template")
	f.StringVar(&flagMcpNetwork, "network", "project_internet", "network mode of the sandboxes: airgapped, project, internet, project_internet")
	rootCmd.AddCommand(mcpCmd)
}
