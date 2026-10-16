package cmd

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
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
	flagLsStatus       []string
	flagLsLineage      string
	flagLsParent       string
	flagLsLimit        int
	flagLsBefore       string
	flagSetName        string
	flagSetIdleTtl     time.Duration
	flagSetAbsoluteTtl time.Duration
	flagWakeRedeliver  bool
	flagForkCount      int
	flagForkNames      []string
)

var newCmd = &cobra.Command{
	Use:     "new [name] [-- cmd args...]",
	Aliases: []string{"create"},
	Short:   "Create a sandbox",
	Long: `Create a sandbox from a base (--base) or a template (--template). With neither, the workspace's only ready base is used.
Ports are declared as port[/protocol][/access], for example 3000, 3000/public, 5432/tcp/project. Access is one of members (default), project, ticket, token, public.
Arguments after -- replace the image's command.`,
	Example: `  zbox new app --base ubuntu -- sleep infinity
  zbox new web --template dev -p 3000/public
  id=$(zbox new -q --template dev)`,
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
			return usagef("at most one name, put the command after --")
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
			return usagef("--base and --template are exclusive")
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
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List sandboxes, newest first",
	Long:    "The list is every matching sandbox unless --limit says otherwise; --before <id> continues with the sandboxes created before that one, so the last id of one page starts the next.",
	Example: `  zbox list
  zbox list --status running,parked --limit 20
  zbox remove -y $(zbox list -q --status failed)`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		if err := resolveFlag(cmd, ws, &flagLsParent); err != nil {
			return err
		}
		p := api.GetSandboxListParams{Status: statusList(flagLsStatus), LineageId: optional(flagLsLineage), ParentId: optional(flagLsParent), BeforeId: optional(flagLsBefore)}
		list, err := listSandboxes(cmd, ws, p)
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("ID", "NAME", "STATUS", "NETWORK", "BASE", "CPU", "MEMORY", "PORTS", "CREATED")
			for _, s := range list {
				tb.row(s.Id, dash(str(s.Name)), s.Status, s.NetworkMode, short(s.BaseDigest), s.Cpus, bytesString(s.MemoryBytes), portList(s), when(s.Created))
			}
			if flagLsStatus == nil {
				tb.ifEmpty("No live sandboxes. `zbox new` creates one, `zbox list --status terminated` lists the ended ones.")
			} else {
				tb.ifEmpty("No sandboxes match.")
			}
			tb.print()
			if flagLsLimit > 0 && len(list) == flagLsLimit {
				morePages(list[len(list)-1].Id)
			}
		})
	},
}

// sandboxPageMax is the largest page GET /sandbox-list answers.
const sandboxPageMax = 1000

// listSandboxes reads the sandboxes newest first: every matching row with no --limit (the API pages only when asked to), otherwise as many as asked for, paging back with beforeId when that is more than one page. An unknown --before is the API's sandboxNotFound.
func listSandboxes(cmd *cobra.Command, ws *api.Workspace, p api.GetSandboxListParams) ([]api.BaseSandbox, error) {
	if flagLsLimit <= 0 {
		return ws.Sandboxes(cmd.Context(), p)
	}
	var list []api.BaseSandbox
	for len(list) < flagLsLimit {
		page := min(sandboxPageMax, flagLsLimit-len(list))
		p.Limit = &page
		rows, err := ws.Sandboxes(cmd.Context(), p)
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

var getCmd = &cobra.Command{
	Use:   "get <id|name>",
	Short: "Show one sandbox",
	Example: `  zbox get app
  zbox get app --json`,
	Args: cobra.ExactArgs(1),
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
	Use:     "set <id|name>",
	Short:   "Rename a sandbox or change its TTLs",
	Example: `  zbox set app --name api --idle-ttl 30m`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		req := api.RequestPutSandbox{Name: optional(flagSetName), IdleTtlSeconds: seconds(flagSetIdleTtl), AbsoluteTtlSeconds: seconds(flagSetAbsoluteTtl)}
		if req.Name == nil && req.IdleTtlSeconds == nil && req.AbsoluteTtlSeconds == nil {
			return usagef("nothing to change, give --name, --idle-ttl or --absolute-ttl")
		}
		s, err := ws.UpdateSandbox(cmd.Context(), args[0], req)
		if err != nil {
			return err
		}
		return emit(s, func() { printSandbox(s) })
	},
}

var renameCmd = &cobra.Command{
	Use:     "rename <id|name> <new-name>",
	Aliases: []string{"mv"},
	Short:   "Rename a sandbox",
	Long:    "The name has to be free among the live sandboxes, a taken one is refused. `zbox set --name` does the same beside the TTLs.",
	Example: `  zbox rename app api
  zbox mv app api`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		s, err := ws.UpdateSandbox(cmd.Context(), args[0], api.RequestPutSandbox{Name: &args[1]})
		if err != nil {
			return err
		}
		return emit(s, func() { printSandbox(s) })
	},
}

var rmCmd = &cobra.Command{
	Use:     "remove <id|name>...",
	Aliases: []string{"rm"},
	Short:   "Terminate sandboxes",
	Example: `  zbox remove app
  zbox remove -y app worker
  zbox remove -y $(zbox list -q --status failed)`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		return deleteEach("terminate "+sandboxList(args), "Disk and memory are gone for good.", args, "terminated", func(i int) (*api.BaseOperation, error) {
			op, err := ws.DeleteSandbox(cmd.Context(), args[i])
			if err != nil {
				return nil, err
			}
			return waitFor(cmd, ws, op)
		})
	},
}

var flagParkNoMemory bool

var parkCmd = &cobra.Command{
	Use:   "park <id|name>",
	Short: "Park a sandbox: keep its disk and memory, free its RAM",
	Long:  "Parks a sandbox with a memory image, so the wake resumes it where it was. With --no-memory the guest is shut down instead: everything running inside ends, the disk stays, and the next wake is a cold boot that picks up the current secrets, workspace env and policy.",
	Example: `  zbox park app
  zbox park app --no-memory`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		err = runOperation(cmd, ws, "parked", func() (*api.BaseOperation, error) { return ws.Park(cmd.Context(), args[0], !flagParkNoMemory) })
		return already(cmd, ws, args[0], err, api.BaseSandboxStatusPARKED, "parked")
	},
}

var wakeCmd = &cobra.Command{
	Use:   "wake <id|name>",
	Short: "Wake a parked sandbox",
	Example: `  zbox wake app
  zbox wake app --redeliver-secrets`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		err = runOperation(cmd, ws, "woke", func() (*api.BaseOperation, error) { return ws.Wake(cmd.Context(), args[0], flagWakeRedeliver) })
		if flagWakeRedeliver {
			return err
		}
		return already(cmd, ws, args[0], err, api.BaseSandboxStatusRUNNING, "running")
	},
}

var forkCmd = &cobra.Command{
	Use:     "fork <id|name>",
	Short:   "Fork a sandbox into one or more children that continue from its current state",
	Example: `  zbox fork app --name try-a --name try-b`,
	Args:    cobra.ExactArgs(1),
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
	if op, err = waitFor(cmd, ws, op); err != nil {
		return err
	}
	return emit(op, func() {
		key := cmp.Or(str(op.SandboxId), op.Id)
		result(key, "%s %s%s%s (operation %s)", key, verb(op, past), bootNote(op), tookNote(op), op.Id)
	})
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
		parent, err := waitFor(cmd, ws, &spawn.Operation)
		if err != nil {
			return err
		}
		for i := range spawn.Sandboxes {
			s, err := ws.Sandbox(cmd.Context(), spawn.Sandboxes[i].Id)
			if err != nil {
				return err
			}
			spawn.Sandboxes[i] = *s
			child := childOf(cmd, ws, parent, s.Id)
			notes[s.Id] = bootNote(child) + tookNote(child)
		}
	}
	return emit(spawn, func() {
		for i, s := range spawn.Sandboxes {
			if i > 0 && !flagQuiet {
				fmt.Println()
			}
			var extra []string
			if n := notes[s.Id]; n != "" {
				extra = []string{"Boot", strings.TrimPrefix(n, ", ")}
			} else if flagNoWait {
				extra = []string{"Operation", spawn.Operation.Id + ", `zbox operation wait " + spawn.Operation.Id + "`"}
			}
			printSandbox(&s, extra...)
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
	note("Waiting for the running %s operation %s first.", strings.ToLower(api.MetaValue(err, "kind")), id)
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
	// An unfinished operation (--no-wait) names its kind, `x terminate running` rather than `x running`, which would read as the sandbox's state.
	return lower(op.Kind) + " " + lower(op.Status)
}

// printSandbox is the detail view of one sandbox, extra pairs (the boot receipt of a spawn) go after the status.
func printSandbox(s *api.BaseSandbox, extra ...string) {
	pairs := []string{
		"ID", s.Id,
		"Name", dash(str(s.Name)),
		"Status", string(s.Status),
	}
	if s.FailedReason != nil {
		pairs = append(pairs, "Reason", *s.FailedReason)
	}
	pairs = append(pairs, extra...)
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
		secrets = "delivered, no fork"
	}
	if s.SecretsStale {
		secrets += ", STALE: wake with --redeliver-secrets"
	}
	pairs = append(pairs, "Secrets", secrets, "Policy gen", strconv.Itoa(s.PolicyGeneration))
	fields(append(pairs, portFields(s)...)...)
}

// portFields are the declared ports as rows of the detail view, one per port with its URL; the table of `zbox port list` is the same set.
func portFields(s *api.BaseSandbox) []string {
	ports := declaredPorts(s)
	if len(ports) == 0 {
		return []string{"Ports", "none, `zbox port open " + s.Id + " <port>`"}
	}
	var out []string
	for _, p := range ports {
		out = append(out, "Port", strings.TrimSpace(fmt.Sprintf("%d %s %s %s", p.Port, lower(p.Protocol), lower(p.Access), strings.Join(portUrls(s, p.Port, p.Access), " "))))
	}
	return out
}

// portUrl is the first URL the API derived for a port, the one `zbox open` takes; empty for one that has none (a TCP port, or no workspace domain).
func portUrl(s *api.BaseSandbox, port int) string {
	for _, h := range s.Hosts {
		if h.Port == port {
			return h.Url
		}
	}
	return ""
}

// portUrlUnder is the URL of a port under one workspace domain, in either form, empty when the domain is not one of the workspace's.
func portUrlUnder(s *api.BaseSandbox, port int, domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	for _, h := range s.Hosts {
		if h.Port != port {
			continue
		}
		if u, err := url.Parse(h.Url); err == nil && (strings.HasSuffix(u.Host, "-"+domain) || strings.HasSuffix(u.Host, "."+domain)) {
			return h.Url
		}
	}
	return ""
}

// portUrls are every URL of a port as the user opens it, one per workspace domain in the API's order.
func portUrls(s *api.BaseSandbox, port int, access api.BaseIngressPortAccess) []string {
	var out []string
	for _, h := range s.Hosts {
		if h.Port == port {
			out = append(out, openUrl(h.Url, access, ""))
		}
	}
	return out
}

// lower prints an enum of the API in lower case, the way the flags take it.
func lower[T ~string](v T) string {
	return strings.ToLower(string(v))
}

// printPorts lists the policy's ports, which is the declared set, with the URL the API derived for the HTTP ones. It says what to do about an empty set instead of printing nothing,
// a sandbox with no port is the normal starting state rather than a mistake.
func printPorts(s *api.BaseSandbox) {
	ports := declaredPorts(s)
	if len(ports) == 0 {
		note("No ports declared, `zbox port open %s <port>` opens one.", s.Id)
		return
	}
	tb := newTable("PORT", "PROTOCOL", "ACCESS", "WAKE", "URL")
	for _, p := range ports {
		tb.row(p.Port, lower(p.Protocol), lower(p.Access), yesNo(wakes(p)), dash(strings.Join(portUrls(s, p.Port, p.Access), " ")))
	}
	tb.print()
}

// openUrl is the URL of a port as the user opens it: the router takes a fresh credential once as a query
// parameter, exchanges it for a session cookie and redirects to the clean URL. MEMBERS and TICKET ports
// take a ticket, TOKEN ports a token, appended when one is given (`zbox cred issue` and `zbox open` mint
// them; a listing has none and prints the bare URL). A MEMBERS port takes no workspace key at all since
// 2026-10-01. PUBLIC ports stay bare and PROJECT ports are not reachable through the edge.
func openUrl(url string, access api.BaseIngressPortAccess, secret string) string {
	if url == "" {
		return ""
	}
	switch access {
	case api.BaseIngressPortAccessMEMBERS, api.BaseIngressPortAccessTICKET:
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

// statusList turns the repeatable --status values (and comma-separated ones in a single flag) into the API's list, upper-cased; none leaves the parameter out, which lists the live sandboxes. An unknown value is the API's 400 to report, the valid set is in the flag help.
func statusList(in []string) *[]string {
	var out []string
	for _, v := range in {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, string(upper[string](s)))
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return &out
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
			return nil, usagef("%q is not KEY=value", p)
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
			ref != "" && (str(b.Name) == ref || b.Digest == ref || b.ImageRef == ref || strings.HasPrefix(strings.TrimPrefix(b.Digest, "sha256:"), ref)):
			matches = append(matches, b)
		}
	}
	switch {
	case len(matches) == 1:
		return &matches[0], nil
	case len(matches) == 0 && ref == "":
		return nil, notFoundf("the workspace has no ready base, add one with `zbox base add <image>` or `zbox base build`")
	case len(matches) == 0:
		return nil, notFoundf("no base matches %q, see `zbox base list`", ref)
	case ref == "":
		return nil, usagef("the workspace has several ready bases, pick one with --base (`zbox base list`)")
	}
	return nil, usagef("%q matches several bases, give more of the digest", ref)
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
		return nil, notFoundf("no template matches %q, see `zbox template list`", ref)
	}
	return nil, usagef("%q matches several templates, give more of the digest", ref)
}

func init() {
	parkCmd.Flags().BoolVar(&flagParkNoMemory, "no-memory", false, "shut the guest down instead of keeping a memory image, the next wake is a cold boot")
	f := newCmd.Flags()
	f.StringVar(&flagNewBase, "base", "", "base to boot from: digest, digest prefix or image ref")
	f.StringVar(&flagNewTemplate, "template", "", "template to spawn from: digest, digest prefix or name")
	f.StringVar(&flagNewNetwork, "network", "project_internet", "network mode, fixed for the sandbox's life: airgapped, project, internet, project_internet")
	f.IntVar(&flagNewCpus, "cpus", 0, "cpu count")
	f.StringVar(&flagNewMemory, "memory", "", "memory cap, for example 512m or 2g, a multiple of 16m from 64m up; the workspace default (512m) when omitted")
	f.DurationVar(&flagNewIdleTtl, "idle-ttl", 0, "park after this long without activity, for example 30m")
	f.DurationVar(&flagNewAbsoluteTtl, "absolute-ttl", 0, "archive after this long no matter what, for example 24h")
	f.StringArrayVarP(&flagNewPorts, "port", "p", nil, "port to expose as port[/protocol][/access], repeatable")
	f.StringArrayVar(&flagNewIntegration, "integration", nil, "integration to attach as name or label=name, repeatable")
	f.StringVar(&flagNewSecrets, "secrets", "", "which workspace envs to deliver: none, allowlist, all (makes the sandbox secret bearing)")
	f.StringArrayVar(&flagNewSecretNames, "secret", nil, "env name to deliver, repeatable, implies --secrets allowlist")
	f.StringVarP(&flagNewPolicy, "policy", "f", "", "JSON file with the full sandbox policy, - for stdin")
	f.StringVarP(&flagNewWorkdir, "workdir", "w", "", "working directory override")
	f.StringArrayVarP(&flagNewEnv, "env", "e", nil, "launch env override as KEY=value, repeatable")
	waits(newCmd, "return right after the create request, with the operation")

	lsCmd.Flags().StringArrayVar(&flagLsStatus, "status", nil, "only sandboxes in any of these statuses, repeatable or comma-separated: creating, running, parking, parked, waking, forking, restoring, failed, terminated, archived (default: the live ones, terminated and archived excluded)")
	lsCmd.Flags().StringVar(&flagLsLineage, "lineage", "", "only sandboxes of this lineage")
	lsCmd.Flags().StringVar(&flagLsParent, "parent", "", "only children of this sandbox")
	lsCmd.Flags().IntVar(&flagLsLimit, "limit", 0, "at most this many, the newest first; every matching sandbox when omitted")
	lsCmd.Flags().StringVar(&flagLsBefore, "before", "", "only sandboxes created before this one (the last id of the previous page)")

	setCmd.Flags().StringVar(&flagSetName, "name", "", "new name")
	setCmd.Flags().DurationVar(&flagSetIdleTtl, "idle-ttl", 0, "new idle TTL")
	setCmd.Flags().DurationVar(&flagSetAbsoluteTtl, "absolute-ttl", 0, "new absolute TTL")

	for _, c := range []*cobra.Command{rmCmd, parkCmd, wakeCmd, forkCmd} {
		waits(c, "return the operation without waiting for it")
	}
	destructive(rmCmd)
	wakeCmd.Flags().BoolVar(&flagWakeRedeliver, "redeliver-secrets", false, "deliver the current secret selection to a stale sandbox")
	forkCmd.Flags().IntVar(&flagForkCount, "count", 0, "number of children, default 1")
	forkCmd.Flags().StringArrayVar(&flagForkNames, "name", nil, "name of a child, repeatable")

	rootCmd.AddCommand(newCmd, lsCmd, getCmd, setCmd, renameCmd, rmCmd, parkCmd, wakeCmd, forkCmd)
}

// waitFor waits for an operation unless --no-wait, bounded by --timeout. A timeout says which operation still runs and how to wait for it again; an operation known only by its id (`operation wait`) has no kind to name.
func waitFor(cmd *cobra.Command, ws *api.Workspace, op *api.BaseOperation) (*api.BaseOperation, error) {
	if flagNoWait {
		return op, nil
	}
	ctx, cancel := waitContext(cmd.Context())
	defer cancel()
	done, err := ws.WaitOperation(ctx, op.Id)
	if err != nil {
		what := "operation " + op.Id + " still runs"
		if op.Kind != "" {
			what = lower(op.Kind) + " " + what
		}
		return done, timedOut(err, what, "`zbox operation wait "+op.Id+"` picks it up")
	}
	return done, nil
}

// named labels a base or a template for a message: the short digest, with the name when it has one.
func named(name *string, digest string) string {
	if name != nil && *name != "" {
		return short(digest) + " (" + *name + ")"
	}
	return short(digest)
}

// morePages tells a caller that asked for a limited page how to ask for the next one.
func morePages(lastId string) {
	note("More may follow: add --before %s for the next page.", lastId)
}

// sandboxList names the sandboxes of a confirmation question.
func sandboxList(ids []string) string {
	if len(ids) == 1 {
		return "sandbox " + ids[0]
	}
	return fmt.Sprintf("%d sandboxes (%s)", len(ids), strings.Join(ids, ", "))
}

// wakes is whether a request to the port wakes a parked sandbox: every access but public does unless the port says otherwise.
func wakes(p api.BaseIngressPort) bool {
	if p.WakeOnRequest != nil {
		return *p.WakeOnRequest
	}
	return p.Access != api.BaseIngressPortAccessPUBLIC
}

// already makes park and wake idempotent: a sandbox that is already where the verb would take it is a success, so a script or an agent can ask for the state it wants without checking first. Under --json the sandbox is printed, there is no operation.
func already(cmd *cobra.Command, ws *api.Workspace, id string, err error, target api.BaseSandboxStatus, word string) error {
	if !api.IsCode(err, "invalidSandboxStatus") || api.MetaValue(err, "status") != string(target) {
		return err
	}
	if flagJson {
		s, err := ws.Sandbox(cmd.Context(), id)
		if err != nil {
			return err
		}
		return printJson(s)
	}
	result(id, "%s already %s", id, word)
	return nil
}
