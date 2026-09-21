package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagNewBase        string
	flagNewTemplate    string
	flagNewNetwork     string
	flagNewCpus        int
	flagNewMemory      string
	flagNewIdleTtl     time.Duration
	flagNewAbsoluteTtl time.Duration
	flagNewPorts       []string
	flagNewIntegration []string
	flagNewSecrets     string
	flagNewSecretNames []string
	flagNewPolicy      string
	flagNewWorkdir     string
	flagNewEnv         []string
	flagNoWait         bool
	flagLsStatus       string
	flagLsLineage      string
	flagLsParent       string
	flagSetName        string
	flagSetIdleTtl     time.Duration
	flagSetAbsoluteTtl time.Duration
	flagWakeRedeliver  bool
	flagForkCount      int
	flagForkNames      []string
)

var newCmd = &cobra.Command{
	Use:   "new [name] [-- cmd args...]",
	Short: "Create a sandbox",
	Long: `Create a sandbox from a base (--base) or a template (--template). With neither, the workspace's only ready base is used.
Ports are declared as port[/protocol][/access], for example 3000, 3000/public, 5432/tcp/project. Access is one of members (default), project, ticket, token, public.
Arguments after -- replace the image's command.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		ws, err := ensureWorkspace(ctx)
		if err != nil {
			return err
		}
		req := api.RequestPostSandbox{NetworkMode: upper[api.RequestPostSandboxNetworkMode](flagNewNetwork)}
		split := cmd.ArgsLenAtDash()
		var positional, launch []string
		if split < 0 {
			positional = args
		} else {
			positional, launch = args[:split], args[split:]
		}
		if len(positional) > 1 {
			return errors.New("at most one name, put the command after --")
		}
		if len(positional) == 1 {
			req.Name = &positional[0]
		}
		if flagNewPolicy != "" {
			req.Policy = &api.BaseSandboxPolicy{}
			if err := readJson(flagNewPolicy, req.Policy); err != nil {
				return err
			}
		}
		switch {
		case flagNewBase != "" && flagNewTemplate != "":
			return errors.New("--base and --template are exclusive")
		case flagNewTemplate != "":
			t, err := findTemplate(cmd, ws, flagNewTemplate)
			if err != nil {
				return err
			}
			req.TemplateDigest = &t.Digest
		default:
			b, err := findBase(cmd, ws, flagNewBase)
			if err != nil {
				return err
			}
			req.BaseDigest = &b.Digest
		}
		req.Cpus = optionalInt(flagNewCpus)
		if flagNewMemory != "" {
			n, err := parseBytes(flagNewMemory)
			if err != nil {
				return err
			}
			req.MemoryBytes = &n
		}
		req.IdleTtlSeconds = seconds(flagNewIdleTtl)
		req.AbsoluteTtlSeconds = seconds(flagNewAbsoluteTtl)
		if len(flagNewPorts) > 0 {
			ports, err := parsePorts(flagNewPorts)
			if err != nil {
				return err
			}
			if req.Policy == nil {
				req.Policy = &api.BaseSandboxPolicy{}
			}
			if req.Policy.Ingress == nil {
				req.Policy.Ingress = &api.BaseIngress{}
			}
			req.Policy.Ingress.Ports = mergePorts(req.Policy.Ingress.Ports, ports)
		}
		if flagNewSecrets != "" || len(flagNewSecretNames) > 0 {
			mode := flagNewSecrets
			if mode == "" || (mode == "allowlist" && len(flagNewSecretNames) == 0) {
				mode = "allowlist"
			}
			if mode == "all" {
				mode = "all_envs"
			}
			if req.Policy == nil {
				req.Policy = &api.BaseSandboxPolicy{}
			}
			req.Policy.Secrets = &api.BaseSecrets{Mode: upper[api.BaseSecretsMode](mode)}
			if len(flagNewSecretNames) > 0 {
				req.Policy.Secrets.Names = &flagNewSecretNames
			}
		}
		for _, spec := range flagNewIntegration {
			att, err := parseAttachment(spec)
			if err != nil {
				return err
			}
			req.Integrations = append(req.Integrations, api.InDtoBodyPostIntegrationAttachment(att))
		}
		if len(launch) > 0 || flagNewWorkdir != "" || len(flagNewEnv) > 0 {
			req.Launch = &api.BaseLaunchContract{}
			if len(launch) > 0 {
				req.Launch.Cmd = &launch
			}
			req.Launch.WorkingDir = optional(flagNewWorkdir)
			if len(flagNewEnv) > 0 {
				env, err := parseEnv(flagNewEnv)
				if err != nil {
					return err
				}
				req.Launch.Env = &env
			}
		}
		return runSpawn(cmd, ws, func() (*api.ResponseSpawn, error) { return ws.CreateSandbox(ctx, req) })
	},
}

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List sandboxes",
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		p := api.GetSandboxListParams{Status: upperPtr[api.GetSandboxListParamsStatus](flagLsStatus), LineageId: optional(flagLsLineage), ParentId: optional(flagLsParent)}
		list, err := ws.Sandboxes(cmd.Context(), p)
		if err != nil {
			return err
		}
		return emit(list, func() {
			table("ID\tNAME\tSTATUS\tNETWORK\tBASE\tAGE\tPORTS", func(w io.Writer) {
				for _, s := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Id, dash(str(s.Name)), s.Status, s.NetworkMode, short(s.BaseDigest), age(s.Created), portList(s))
				}
			})
		})
	},
}

var getCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		s, err := ws.Sandbox(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return emit(s, func() { printSandbox(s) })
	},
}

var setCmd = &cobra.Command{
	Use:   "set <id>",
	Short: "Rename a sandbox or change its TTLs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPutSandbox{Name: optional(flagSetName), IdleTtlSeconds: seconds(flagSetIdleTtl), AbsoluteTtlSeconds: seconds(flagSetAbsoluteTtl)}
		if req.Name == nil && req.IdleTtlSeconds == nil && req.AbsoluteTtlSeconds == nil {
			return errors.New("nothing to change, give --name, --idle-ttl or --absolute-ttl")
		}
		s, err := ws.UpdateSandbox(cmd.Context(), args[0], req)
		if err != nil {
			return err
		}
		return emit(s, func() { printSandbox(s) })
	},
}

var rmCmd = &cobra.Command{
	Use:   "rm <id>...",
	Short: "Terminate sandboxes",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		var ops []api.BaseOperation
		for _, id := range args {
			op, err := ws.DeleteSandbox(cmd.Context(), id)
			if err != nil {
				return err
			}
			if !flagNoWait {
				if op, err = ws.WaitOperation(cmd.Context(), op.Id); err != nil {
					return err
				}
			}
			ops = append(ops, *op)
			if !flagJson {
				fmt.Printf("%s %s\n", id, verb(op, "terminated"))
			}
		}
		return emit(ops, func() {})
	},
}

var parkCmd = &cobra.Command{
	Use:   "park <id>",
	Short: "Park a sandbox: keep its disk and memory, free its RAM",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		return runOperation(cmd, ws, "parked", func() (*api.BaseOperation, error) { return ws.Park(cmd.Context(), args[0]) })
	},
}

var wakeCmd = &cobra.Command{
	Use:   "wake <id>",
	Short: "Wake a parked sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		return runOperation(cmd, ws, "woke", func() (*api.BaseOperation, error) { return ws.Wake(cmd.Context(), args[0], flagWakeRedeliver) })
	},
}

var forkCmd = &cobra.Command{
	Use:   "fork <id>",
	Short: "Fork a sandbox into one or more children that continue from its current state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPostSandboxFork{Count: optionalInt(flagForkCount)}
		if len(flagForkNames) > 0 {
			req.Names = &flagForkNames
			if flagForkCount == 0 {
				req.Count = optionalInt(len(flagForkNames))
			}
		}
		return runSpawn(cmd, ws, func() (*api.ResponseSpawn, error) { return ws.Fork(cmd.Context(), args[0], req) })
	},
}

// runOperation runs a mutation that answers with an operation and, unless --no-wait, waits for it and reports the outcome.
func runOperation(cmd *cobra.Command, ws *api.Workspace, past string, start func() (*api.BaseOperation, error)) error {
	op, err := start()
	if err = afterInFlight(cmd, ws, err); err == nil && op == nil {
		op, err = start()
	}
	if err != nil {
		return err
	}
	if !flagNoWait {
		if op, err = ws.WaitOperation(cmd.Context(), op.Id); err != nil {
			return err
		}
	}
	return emit(op, func() { fmt.Printf("%s %s%s (operation %s)\n", str(op.SandboxId), verb(op, past), bootNote(op), op.Id) })
}

// bootNote reads the receipt of a finished boot (CREATE, WAKE, FORK, RESTORE): the result is a free object in the contract, resumedMemory says whether the guest resumed from a memory image instead of cold booting.
func bootNote(op *api.BaseOperation) string {
	if op == nil || op.Result == nil {
		return ""
	}
	resumed, ok := (*op.Result)["resumedMemory"].(bool)
	if !ok {
		return ""
	}
	if resumed {
		return ", resumed from memory"
	}
	return ", cold boot"
}

// runSpawn handles fork and restore, which answer with a bulk operation and the new rows. The wait is on the parent operation, then the children are re-read.
func runSpawn(cmd *cobra.Command, ws *api.Workspace, start func() (*api.ResponseSpawn, error)) error {
	spawn, err := start()
	if err = afterInFlight(cmd, ws, err); err == nil && spawn == nil {
		spawn, err = start()
	}
	if err != nil {
		return err
	}
	notes := map[string]string{}
	if !flagNoWait {
		parent, err := ws.WaitOperation(cmd.Context(), spawn.Operation.Id)
		if err != nil {
			return err
		}
		for i := range spawn.Sandboxes {
			s, err := ws.Sandbox(cmd.Context(), spawn.Sandboxes[i].Id)
			if err != nil {
				return err
			}
			spawn.Sandboxes[i] = *s
			notes[s.Id] = bootNote(childOf(cmd, ws, parent, s.Id))
		}
	}
	return emit(spawn, func() {
		for _, s := range spawn.Sandboxes {
			printSandbox(&s)
			if n := notes[s.Id]; n != "" {
				fmt.Println("Boot:", strings.TrimPrefix(n, ", "))
			}
			fmt.Println()
		}
	})
}

// childOf finds the boot operation of one spawned row: the child of the bulk operation that names the sandbox, or the operation itself when it is the sandbox's own (a single create). Nothing found leaves the receipt out, the rows are already printed.
func childOf(cmd *cobra.Command, ws *api.Workspace, parent *api.BaseOperation, sandboxId string) *api.BaseOperation {
	if parent.SandboxId != nil && *parent.SandboxId == sandboxId {
		return parent
	}
	sid := api.SandboxId(sandboxId)
	ops, err := ws.Operations(cmd.Context(), api.GetOperationListParams{SandboxId: &sid})
	if err != nil {
		return nil
	}
	for i := range ops {
		if ops[i].ParentId != nil && *ops[i].ParentId == parent.Id {
			return &ops[i]
		}
	}
	return nil
}

// afterInFlight turns an operationInFlight answer into a wait on that operation, so a park right after a wake queues instead of failing. The caller retries once when it returns nil.
// With --no-wait the answer is reported as it came, the caller asked not to sit around.
func afterInFlight(cmd *cobra.Command, ws *api.Workspace, err error) error {
	if err == nil || flagNoWait || !api.IsCode(err, "operationInFlight") {
		return err
	}
	id := api.MetaValue(err, "operationId")
	if id == "" {
		return err
	}
	if !flagJson {
		fmt.Fprintf(os.Stderr, "waiting for the running %s operation %s first\n", strings.ToLower(api.MetaValue(err, "kind")), id)
	}
	// The outcome of the other operation is not ours to judge, the retry answers for the state it left behind.
	if _, err := ws.WaitOperation(cmd.Context(), id); err != nil && !errors.As(err, new(api.OperationFailed)) {
		return err
	}
	return nil
}

func verb(op *api.BaseOperation, past string) string {
	switch op.Status {
	case api.BaseOperationStatusFINISHED:
		return past
	case api.BaseOperationStatusFAILED:
		return "failed: " + dash(str(op.Error))
	}
	return strings.ToLower(string(op.Status))
}

func printSandbox(s *api.BaseSandbox) {
	pairs := []string{
		"ID", s.Id,
		"Name", dash(str(s.Name)),
		"Status", string(s.Status),
	}
	if s.FailedReason != nil {
		pairs = append(pairs, "Reason", *s.FailedReason)
	}
	pairs = append(pairs,
		"Network", string(s.NetworkMode),
		"Base", short(s.BaseDigest),
	)
	if s.TemplateDigest != nil {
		pairs = append(pairs, "Template", short(*s.TemplateDigest))
	}
	pairs = append(pairs, "Resources", fmt.Sprintf("%d cpu, %s", s.Cpus, bytesString(s.MemoryBytes)))
	if s.ParentId != nil {
		pairs = append(pairs, "Parent", *s.ParentId)
	}
	pairs = append(pairs, "Lineage", s.LineageId, "Created", when(s.Created), "Activity", when(s.LastActivity))
	if s.IdleTtlSeconds != nil {
		pairs = append(pairs, "Idle TTL", fmt.Sprintf("%s, parks at %s", (time.Duration(*s.IdleTtlSeconds)*time.Second).String(), whenPtr(s.IdleDeadline)))
	}
	if s.AbsoluteTtlSeconds != nil {
		pairs = append(pairs, "Absolute TTL", fmt.Sprintf("%s, ends at %s", (time.Duration(*s.AbsoluteTtlSeconds)*time.Second).String(), whenPtr(s.AbsoluteDeadline)))
	}
	if s.StableAddress != nil {
		pairs = append(pairs, "Address", *s.StableAddress)
	}
	secrets := "none"
	if s.SecretBearing {
		secrets = "delivered, no memory park or fork"
		if s.SecretsStale {
			secrets += ", STALE: wake with --redeliver-secrets"
		}
	}
	pairs = append(pairs, "Secrets", secrets, "Policy gen", strconv.Itoa(s.PolicyGeneration))
	fields(pairs...)
	printPorts(s)
}

// printPorts lists the policy's ports, which is the declared set, with the URL the API derived for the HTTP ones. It says what to do about an empty set instead of printing nothing,
// a sandbox with no port is the normal starting state rather than a mistake.
func printPorts(s *api.BaseSandbox) {
	ports := declaredPorts(s)
	if len(ports) == 0 {
		fmt.Printf("Ports:   none, open one with `zbox port open %s <port>`\n", s.Id)
		return
	}
	urls := map[int]string{}
	for _, h := range s.Hosts {
		urls[h.Port] = h.Url
	}
	table("PORT\tPROTOCOL\tACCESS\tURL", func(w io.Writer) {
		for _, p := range ports {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", p.Port, strings.ToLower(string(p.Protocol)), strings.ToLower(string(p.Access)), dash(openUrl(urls[p.Port], p.Access, "")))
		}
	})
}

// openUrl is the URL of a port as the user opens it: the router takes the credential once as a query
// parameter, exchanges it for a session cookie and redirects to the clean URL. MEMBERS ports carry the
// workspace key, TICKET and TOKEN ports carry the secret when one is given (only `zbox cred issue`
// knows it), PUBLIC ports stay bare and PROJECT ports are not reachable through the edge.
func openUrl(url string, access api.BaseIngressPortAccess, secret string) string {
	if url == "" {
		return ""
	}
	switch access {
	case api.BaseIngressPortAccessMEMBERS:
		if workspaceKey != "" {
			return url + "?apiKey=" + workspaceKey
		}
	case api.BaseIngressPortAccessTICKET:
		if secret != "" {
			return url + "?ticket=" + secret
		}
	case api.BaseIngressPortAccessTOKEN:
		if secret != "" {
			return url + "?token=" + secret
		}
	}
	return url
}

func declaredPorts(s *api.BaseSandbox) []api.BaseIngressPort {
	if s.Policy.Ingress == nil {
		return nil
	}
	return s.Policy.Ingress.Ports
}

func portList(s api.BaseSandbox) string {
	ports := declaredPorts(&s)
	if len(ports) == 0 {
		return "-"
	}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, strconv.Itoa(p.Port))
	}
	return strings.Join(out, ",")
}

func parseEnv(pairs []string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	for _, p := range pairs {
		k, v, found := strings.Cut(p, "=")
		if !found || k == "" {
			return nil, fmt.Errorf("%q is not KEY=value", p)
		}
		out[k] = v
	}
	return out, nil
}

// findBase resolves a digest, a digest prefix or an image ref to a base, and with nothing given picks the only ready base of the workspace.
func findBase(cmd *cobra.Command, ws *api.Workspace, ref string) (*api.BaseBase, error) {
	bases, err := ws.Bases(cmd.Context())
	if err != nil {
		return nil, err
	}
	var matches []api.BaseBase
	for _, b := range bases {
		switch {
		case ref == "" && b.Status == api.BaseBaseStatusREADY,
			ref != "" && (b.Digest == ref || b.ImageRef == ref || strings.HasPrefix(strings.TrimPrefix(b.Digest, "sha256:"), ref)):
			matches = append(matches, b)
		}
	}
	switch {
	case len(matches) == 1:
		return &matches[0], nil
	case len(matches) == 0 && ref == "":
		return nil, errors.New("the workspace has no ready base, add one with `zbox base add <image>`")
	case len(matches) == 0:
		return nil, fmt.Errorf("no base matches %q, see `zbox base ls`", ref)
	case ref == "":
		return nil, errors.New("the workspace has several bases, pick one with --base")
	}
	return nil, fmt.Errorf("%q matches several bases, give more of the digest", ref)
}

func findTemplate(cmd *cobra.Command, ws *api.Workspace, ref string) (*api.BaseTemplate, error) {
	templates, err := ws.Templates(cmd.Context())
	if err != nil {
		return nil, err
	}
	var matches []api.BaseTemplate
	for _, t := range templates {
		if t.Digest == ref || str(t.Name) == ref || strings.HasPrefix(strings.TrimPrefix(t.Digest, "sha256:"), ref) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		return nil, fmt.Errorf("no template matches %q, see `zbox template ls`", ref)
	}
	return nil, fmt.Errorf("%q matches several templates, give more of the digest", ref)
}

func init() {
	f := newCmd.Flags()
	f.StringVar(&flagNewBase, "base", "", "base to boot from: digest, digest prefix or image ref")
	f.StringVar(&flagNewTemplate, "template", "", "template to spawn from: digest, digest prefix or name")
	f.StringVar(&flagNewNetwork, "network", "project_internet", "network mode, fixed for the sandbox's life: airgapped, project, internet, project_internet")
	f.IntVar(&flagNewCpus, "cpus", 0, "cpu count")
	f.StringVar(&flagNewMemory, "memory", "", "memory cap, for example 512m or 2g")
	f.DurationVar(&flagNewIdleTtl, "idle-ttl", 0, "park after this long without activity, for example 30m")
	f.DurationVar(&flagNewAbsoluteTtl, "absolute-ttl", 0, "archive after this long no matter what, for example 24h")
	f.StringArrayVarP(&flagNewPorts, "port", "p", nil, "port to expose as port[/protocol][/access], repeatable")
	f.StringArrayVar(&flagNewIntegration, "integration", nil, "integration to attach as name or label=name, repeatable")
	f.StringVar(&flagNewSecrets, "secrets", "", "which workspace envs to deliver: none, allowlist, all (makes the sandbox secret bearing)")
	f.StringArrayVar(&flagNewSecretNames, "secret", nil, "env name to deliver, repeatable, implies --secrets allowlist")
	f.StringVarP(&flagNewPolicy, "policy", "f", "", "JSON file with the full sandbox policy, - for stdin")
	f.StringVarP(&flagNewWorkdir, "workdir", "w", "", "working directory override")
	f.StringArrayVarP(&flagNewEnv, "env", "e", nil, "launch env override as KEY=value, repeatable")
	f.BoolVar(&flagNoWait, "no-wait", false, "return right after the create request")

	lsCmd.Flags().StringVar(&flagLsStatus, "status", "", "only sandboxes in this status")
	lsCmd.Flags().StringVar(&flagLsLineage, "lineage", "", "only sandboxes of this lineage")
	lsCmd.Flags().StringVar(&flagLsParent, "parent", "", "only children of this sandbox")

	setCmd.Flags().StringVar(&flagSetName, "name", "", "new name")
	setCmd.Flags().DurationVar(&flagSetIdleTtl, "idle-ttl", 0, "new idle TTL")
	setCmd.Flags().DurationVar(&flagSetAbsoluteTtl, "absolute-ttl", 0, "new absolute TTL")

	for _, c := range []*cobra.Command{rmCmd, parkCmd, wakeCmd, forkCmd} {
		c.Flags().BoolVar(&flagNoWait, "no-wait", false, "return the operation without waiting for it")
	}
	wakeCmd.Flags().BoolVar(&flagWakeRedeliver, "redeliver-secrets", false, "deliver the current secret selection to a stale sandbox")
	forkCmd.Flags().IntVar(&flagForkCount, "count", 0, "number of children, default 1")
	forkCmd.Flags().StringArrayVar(&flagForkNames, "name", nil, "name of a child, repeatable")

	rootCmd.AddCommand(newCmd, lsCmd, getCmd, setCmd, rmCmd, parkCmd, wakeCmd, forkCmd)
}
