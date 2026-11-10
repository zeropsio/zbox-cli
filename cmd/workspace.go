package cmd

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zeropsio/zerops-go/dto/output"
	"github.com/zeropsio/zerops-go/types/enum"

	"github.com/zeropsio/zbox-cli/internal/api"
	"github.com/zeropsio/zbox-cli/internal/browser"
	"github.com/zeropsio/zbox-cli/internal/config"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

var (
	flagWorkspaceKey  string
	flagWorkspaceName string
	flagPolicyFile    string
	flagDomainForm    string
	flagOpenDomain    string
)

var workspaceCmd = &cobra.Command{
	Use:     "workspace",
	Aliases: []string{"ws"},
	Short:   "Show the workspace, or manage the workspaces with the subcommands",
	Long:    "Without a subcommand the current workspace is described: API, domains, version, capacity, what is in use, the host facts and the policy. The subcommands manage the workspaces this machine knows (list, switch, authorize, use, forget), the current one (status, policy, domain, key, console to open it in Zerops Box) and its life (start, stop, restart, reload, resize, teardown); `zbox setup` creates one.",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		info, err := ws.Info(cmd.Context())
		if err != nil {
			return err
		}
		return emit(info, func() {
			fields(append([]string{
				"API", ws.BaseUrl,
				"Domains", domainsString(info.Domains),
				"Version", info.Version,
				"Substrate", lower(info.Substrate),
				"Image", dash(str(info.ImageRelease)),
				"Capacity", capacityString(info.Capacity),
				"In use", usageString(info.Usage),
				"Always denied", dash(strings.Join(info.AlwaysDenied, ", ")),
			}, hostFields(info.Host)...)...)
			section("Policy", info.Policy)
		})
	},
}

// hostFields is what the orchestrator learnt about the workspace VM, the facts to compare when two workspaces behave differently.
func hostFields(h api.BaseWorkspaceHost) []string {
	nested := "svm"
	if h.CpuVendor != api.AMD {
		var flags []string
		for _, f := range []struct {
			on   bool
			name string
		}{{h.Apicv, "apicv"}, {h.Pml, "pml"}, {h.ShadowVmcs, "shadow_vmcs"}} {
			if f.on {
				flags = append(flags, f.name)
			}
		}
		nested = dash(strings.Join(flags, " "))
	}
	volume := h.VolumeFilesystem + ", not encrypted"
	if h.VolumeEncrypted {
		volume = h.VolumeFilesystem + " under LUKS2"
	}
	image := "not recorded"
	if h.ImageBuilt != nil {
		image = when(*h.ImageBuilt)
		if h.AptSnapshot != nil {
			image += ", apt snapshot " + *h.AptSnapshot
		}
	}
	return []string{
		"Host CPU", fmt.Sprintf("%s (%s)", h.CpuModel, lower(h.CpuVendor)),
		"Host nested virt", nested,
		"Host kernel", h.Kernel,
		"Host firecracker", h.Firecracker,
		"Host guest kernel", fmt.Sprintf("%s (%s)", dash(str(h.GuestKernel)), short(h.GuestKernelDigest)),
		"Host agent", short(h.AgentDigest),
		"Host image built", image,
		"Host data volume", fmt.Sprintf("%s on %s", volume, h.VolumeDevice),
		"Host memory", fmt.Sprintf("%s restores, thp %s", h.RestoreBacking, h.TransparentHugepages),
		"Host slots", fmt.Sprintf("%d of %d", h.SlotsUsed, h.SlotsLimit),
		"Host started", when(h.StartedAt),
	}
}

// capacityString is the admission envelope: the vCPUs the sandboxes may reserve over the cores, the memory they may reserve and the
// reserve taken off the VM's memory for it, the data volume with the headroom a verb must leave.
func capacityString(c api.BaseWorkspaceCapacity) string {
	return fmt.Sprintf("%d vcpu (%d cores x %g), %s memory (%s less %s reserve), %s disk (%s headroom)",
		int(float64(c.Cpus)*float64(c.CpuOvercommit)), c.Cpus, c.CpuOvercommit,
		bytesString(c.MemoryBytes), bytesString(c.MemoryTotalBytes), bytesString(c.MemoryReserveBytes),
		bytesString(c.DiskBytes), bytesString(c.DiskHeadroomBytes))
}

// usageString is what is reserved of the capacity (every running sandbox reserves its cap in full, a build its share), what the sandboxes
// hold right now, and what the data volume holds across bases, templates, sandboxes and snapshots.
func usageString(u api.BaseWorkspaceUsage) string {
	noun := "sandboxes"
	if u.RunningSandboxes == 1 {
		noun = "sandbox"
	}
	builds := ""
	if u.Builds > 0 {
		builds = fmt.Sprintf(" and %d builds", u.Builds)
	}
	return fmt.Sprintf("%d vcpu, %s memory reserved by %d running %s%s (%d parked), %s resident, %s disk",
		u.Cpus, bytesString(u.MemoryBytes), u.RunningSandboxes, noun, builds, u.ParkedSandboxes, bytesString(u.MemoryResidentBytes), bytesString(u.DiskBytes))
}

var workspacePolicyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Print the workspace policy, or replace it with -f",
	Long:  "Without -f the policy is printed as JSON. With -f the JSON file (or - for stdin) replaces the whole policy, so start from the printed one.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		if flagPolicyFile == "" {
			info, err := ws.Info(cmd.Context())
			if err != nil {
				return err
			}
			return printJson(info.Policy)
		}
		var body api.RequestPutWorkspacePolicy
		if err := readJson(flagPolicyFile, &body); err != nil {
			return err
		}
		info, err := ws.PutWorkspacePolicy(cmd.Context(), body)
		if err != nil {
			return err
		}
		return emit(info, func() { say("Workspace policy updated.") })
	},
}

var workspaceUseCmd = &cobra.Command{
	Use:   "use <api-url>",
	Short: "Point the CLI at a workspace by its API URL and key or token, outside the Zerops account",
	Long:  "The escape hatch for a workspace the Zerops login does not reach: a script with the key of its own workspace, another account's service, a backend with a scoped token from `zbox token create`. The orchestrator is called at the URL with the key or the token as the bearer, nothing goes through zproxy. A token answers forbidden on every route its scopes do not cover. A workspace of the account is stored with `zbox workspace authorize <service-id>` instead and needs no key.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagWorkspaceKey == "" {
			return usagef("--api-key is required, the ZBOX_WORKSPACE_API_KEY env of the workspace service or a token from `zbox token create`")
		}
		c, err := loadConfig()
		if err != nil {
			return err
		}
		url := strings.TrimSuffix(args[0], "/")
		if !strings.Contains(url, "://") {
			url = "https://" + url
		}
		info, err := api.New(url, flagWorkspaceKey).Info(cmd.Context())
		if err != nil {
			return fmt.Errorf("the workspace did not answer: %w", err)
		}
		// A workspace pointed at by hand has no Zerops ids the CLI could tear down.
		w := config.Workspace{Name: cmp.Or(flagWorkspaceName, config.DefaultName(url)), WorkspaceUrl: url, ApiKey: flagWorkspaceKey}
		// Named for one run, the workspace is only stored and the current one stays. The name has to be its own, so a ZBOX_WORKSPACE left in the environment cannot pass for a request to keep the selection.
		if ref, source := workspaceRef(); ref != "" {
			known := []config.Workspace{w}
			for _, other := range c.Workspaces {
				if !other.Same(w) {
					known = append(known, other)
				}
			}
			if named, err := (config.Config{Workspaces: known}).Lookup(ref); err != nil || !named.Same(w) {
				return usagef("%s %s does not name the workspace being stored (%s, %s); name it, or run without to make it the current one", source, ref, w.Name, w.Host())
			}
			if err := c.Remember(w); err != nil {
				return err
			}
			line := fmt.Sprintf("Stored workspace %s: %s (%s on %s).", w.Name, url, info.Version, lower(info.Substrate))
			if current, ok := c.Current(); ok && !current.Same(w) {
				line += fmt.Sprintf(" The current one stays %s (%s).", current.Name, current.Label())
			}
			result(w.Name, "%s", line)
			return nil
		}
		if err := c.Assign(w); err != nil {
			return err
		}
		result(w.Name, "Using workspace %s: %s (%s on %s).", w.Name, url, info.Version, lower(info.Substrate))
		return nil
	},
}

var workspaceForgetCmd = &cobra.Command{
	Use:   "forget",
	Short: "Drop the workspace assignment without deleting anything",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadSelected()
		if err != nil {
			return err
		}
		forgotten, known := c.Current()
		if err := c.ForgetWorkspace(); err != nil {
			return err
		}
		if known {
			say("Workspace %s (%s) forgotten.", forgotten.Name, forgotten.Label())
		} else {
			say("Workspace forgotten.")
		}
		printOthers(c)
		return nil
	},
}

var workspaceLsCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List the workspaces of the Zerops account, with the ones stored here",
	Long:    "Every Box Workspace service (zbox@1) in every organisation of the account is a workspace; an ORGANIZATION column is added when the account is a member of more than one. A row with a name is stored here and can be switched to; the others need `zbox workspace authorize <service-id>` first, which stores them without any secret. Workspaces assigned with `zbox workspace use` that are not in the account are listed after. A project or organisation that the account has no access to, or that is gone, is left out. Without a login only the stored ones are listed. --json prints {account, known}: the services of the account and the workspaces stored here, without their keys and access tokens.",
	Example: `  zbox workspace list
  zbox workspace list -q
  zbox workspace list --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		zc, err := zeropsClient(cmd.Context(), &c)
		if err != nil {
			return emit(withoutSecrets(c.Workspaces), func() {
				printWorkspaces(c)
				note("Not logged in, only the workspaces known here are listed. Run `zbox login` to see the account's.")
			})
		}
		user, err := zc.UserInfo(cmd.Context())
		if err != nil {
			return err
		}
		stacks, skipped, err := zc.Workspaces(cmd.Context(), user.Organizations)
		if err != nil {
			return err
		}
		return emit(map[string]any{"account": stacks, "known": withoutSecrets(c.Workspaces)}, func() {
			printAccountWorkspaces(c, stacks, len(user.Organizations) > 1)
			for _, o := range skipped {
				note("Organisation %s (%s) is left out: the API refused the list of its projects or no longer has the organisation.", o.Name, o.Id)
			}
		})
	},
}

// printAccountWorkspaces is the table of `zbox workspace list`: the services of the account, then the stored workspaces it does not have.
func printAccountWorkspaces(c config.Config, stacks []zerops.WorkspaceStack, withOrg bool) {
	byService := map[string]config.Workspace{}
	for _, w := range c.Workspaces {
		if w.ServiceId != "" {
			byService[w.ServiceId] = w
		}
	}
	// The ORGANIZATION column sits between the service and its project, and only when the account has several.
	header := []string{"", "SERVICE ID", "NAME", "HOSTNAME"}
	org := func(name string) []any { return nil }
	if withOrg {
		header = append(header, "ORGANIZATION")
		org = func(name string) []any { return []any{dash(name)} }
	}
	tb := newTable(append(header, "PROJECT", "STATUS", "STORED")...).keyCol(1)
	listed := map[string]bool{}
	for _, st := range stacks {
		known, ok := byService[st.ServiceId]
		listed[st.ServiceId] = true
		cells := append([]any{mark(c, known), st.ServiceId, dash(known.Name), st.Hostname}, org(st.OrganizationName)...)
		tb.row(append(cells, st.ProjectName, st.Status, map[bool]string{true: "yes", false: "-"}[ok])...)
	}
	for _, ws := range c.Workspaces {
		if ws.ServiceId != "" && listed[ws.ServiceId] {
			continue
		}
		cells := append([]any{mark(c, ws), dash(ws.ServiceId), ws.Name, dash(ws.Host())}, org("")...)
		tb.row(append(cells, dash(ws.ProjectName), "not in this account", "yes")...)
	}
	tb.ifEmpty("No workspaces in the account. `zbox setup` creates one.")
	tb.print()
	printMarks(c)
}

// withoutSecrets is the stored workspaces as --json prints them, see config.Workspace.WithoutSecrets.
func withoutSecrets(ws []config.Workspace) []config.Workspace {
	out := make([]config.Workspace, len(ws))
	for i, w := range ws {
		out[i] = w.WithoutSecrets()
	}
	return out
}

// mark is the asterisk of the current workspace in listings, and the arrow of the one named for this run.
func mark(c config.Config, w config.Workspace) string {
	switch {
	case !w.Usable():
		return " "
	case overridden(c).Same(w):
		return ">"
	case c.IsCurrent(w):
		return "*"
	}
	return " "
}

// overridden is the workspace named for this run, empty when none is or the name does not resolve: a listing is where one looks the name up, so it does not fail on it.
func overridden(c config.Config) config.Workspace {
	ref, _ := workspaceRef()
	if ref == "" {
		return config.Workspace{}
	}
	w, _ := c.Lookup(ref)
	return w
}

// printMarks explains the arrow under a listing, or why there is none.
func printMarks(c config.Config) {
	ref, source := workspaceRef()
	if ref == "" {
		return
	}
	if w, err := c.Lookup(ref); err != nil {
		note("%s: %v", source, err)
	} else {
		note("> is %s (%s %s), the workspace commands work on in this run; * is the current one.", w.Name, source, ref)
	}
}

func printWorkspaces(c config.Config) {
	if len(c.Workspaces) == 0 {
		note("No workspaces known here, run `zbox setup` or `zbox workspace authorize <service-id>`.")
		return
	}
	tb := newTable("", "NAME", "API HOST", "PROJECT", "SERVICE ID").keyCol(1)
	for _, ws := range c.Workspaces {
		tb.row(mark(c, ws), ws.Name, dash(ws.Host()), dash(ws.ProjectName), dash(ws.ServiceId))
	}
	tb.print()
	printMarks(c)
}

var workspaceAuthorizeCmd = &cobra.Command{
	Use:   "authorize <service-id>",
	Short: "Store a workspace of the Zerops account here, no key involved",
	Long:  "The workspace is reached through zproxy: the Zerops login mints an access to it (`POST /service-stack/{id}/ui-access`, BasicUser or above on the service) and zproxy adds the workspace key on the way, so nothing secret is stored here. The command checks the access can be minted, stores the workspace under its hostname (`--name` overrides) and makes it the current one only when none is. `zbox workspace list` lists the service ids.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		zc, err := zeropsClient(cmd.Context(), &c)
		if err != nil {
			return err
		}
		w, err := authorizeWorkspace(cmd.Context(), &c, zc, args[0], flagWorkspaceName)
		if err != nil {
			return err
		}
		if c.IsCurrent(w) {
			result(w.Name, "Stored workspace %s (%s), it is the current one.", w.Name, w.Label())
		} else {
			result(w.Name, "Stored workspace %s (%s). `zbox workspace switch %s` makes it the current one.", w.Name, w.Label(), w.Name)
		}
		return nil
	},
}

// authorizeWorkspace stores the service as a workspace under the name (the service hostname when empty) and mints its first access, which proves the account reaches it. The current workspace stays unless there is none.
func authorizeWorkspace(ctx context.Context, c *config.Config, zc *zerops.Client, serviceId, name string) (config.Workspace, error) {
	stack, err := zc.ServiceStack(ctx, serviceId)
	if err != nil {
		return config.Workspace{}, err
	}
	if name == "" {
		name = stack.Name.Native()
	}
	w := config.Workspace{Name: name, ProjectId: string(stack.ProjectId), ProjectName: stack.Project.Name.Native(), ServiceId: serviceId}
	// A workspace stored before keeps what it had, the access included, under the name given now.
	if known, err := c.Lookup(serviceId); err == nil && known.Same(w) {
		w.WorkspaceUrl, w.Access = known.WorkspaceUrl, known.Access
	}
	if err := c.Remember(w); err != nil {
		return config.Workspace{}, err
	}
	if _, err := mintAccess(ctx, c, zc, w); err != nil {
		return config.Workspace{}, err
	}
	// The API host is kept for the listings and for `zbox workspace switch`, read from the service env the platform writes; a workspace without subdomain access has none.
	if apiHost, _ := subdomainHosts(ctx, zc, serviceId); apiHost != "" {
		w.WorkspaceUrl = "https://" + apiHost
		if known, err := c.Lookup(serviceId); err == nil {
			w.Access = known.Access
		}
		if err := c.Remember(w); err != nil {
			return config.Workspace{}, err
		}
	}
	return w, nil
}

// subdomainHosts are the API host and the ingress host of the service, from the zeropsSubdomain env the platform writes for every published port; both empty without subdomain access or when the env cannot be read.
func subdomainHosts(ctx context.Context, zc *zerops.Client, serviceId string) (string, string) {
	value, found, err := zc.ServiceEnv(ctx, serviceId, "zeropsSubdomain")
	if err != nil || !found {
		return "", ""
	}
	return zerops.SubdomainHosts(value)
}

// domainsString lists the workspace domains the way `zbox workspace domain list` does, one `domain (form)` per entry.
func domainsString(domains []api.BaseWorkspaceDomain) string {
	if len(domains) == 0 {
		return "none, `zbox workspace domain add <domain>` gives sandbox ports a public URL"
	}
	parts := make([]string, 0, len(domains))
	for _, d := range domains {
		parts = append(parts, fmt.Sprintf("%s (%s)", d.Domain, lower(d.Form)))
	}
	return strings.Join(parts, ", ")
}

var workspaceDomainCmd = &cobra.Command{
	Use:   "domain",
	Short: "The domains sandbox ports are published under",
	Long:  "A sandbox port gets one public URL per domain of the workspace, in list order; the first one is what `zbox open` uses. The edge routing is Zerops's side: the dash form `{port}-{id}-<domain>` is for the Zerops subdomain of the ingress port (`zbox setup` adds it when subdomain access is on), the dot form `{port}-{id}.<domain>` for an own wildcard domain, `*.<domain>`, routed to the ingress port 8081 in the Zerops GUI with its DNS records (A, AAAA and the ACME CNAME the GUI shows). A client on the project network needs no domain: it sends the bare `{port}-{id}` as the Host header to `http://<workspace>.zerops:8081/`.",
}

var workspaceDomainLsCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List the domains",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		info, err := ws.Info(cmd.Context())
		if err != nil {
			return err
		}
		return emit(info.Domains, func() { printDomains(info.Domains) })
	},
}

var workspaceDomainAddCmd = &cobra.Command{
	Use:   "add <domain>",
	Short: "Add a domain, in the dot form unless --form dash",
	Example: `  zbox workspace domain add sb.example.com
  zbox workspace domain add zbox-f-8081.app-prg1.zerops.io --form dash`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		form, err := domainForm(flagDomainForm)
		if err != nil {
			return err
		}
		info, err := ws.Info(cmd.Context())
		if err != nil {
			return err
		}
		domain := strings.ToLower(strings.TrimSpace(args[0]))
		for _, d := range info.Domains {
			if d.Domain == domain {
				return usagef("domain %s is already listed (%s)", domain, lower(d.Form))
			}
		}
		info, err = ws.PutWorkspaceDomains(cmd.Context(), append(info.Domains, api.BaseWorkspaceDomain{Domain: domain, Form: form}))
		if err != nil {
			return err
		}
		return emit(info.Domains, func() {
			say("Domain %s added, sandbox ports are published at %s.", domain, domainExample(api.BaseWorkspaceDomain{Domain: domain, Form: form}))
			printDomains(info.Domains)
		})
	},
}

var workspaceDomainRmCmd = &cobra.Command{
	Use:     "remove <domain>...",
	Aliases: []string{"rm"},
	Short:   "Remove domains",
	Long:    "The domains are taken out of the list in one write; one that is not listed fails the command before anything changes. Sandbox ports lose their URLs under them.",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		info, err := ws.Info(cmd.Context())
		if err != nil {
			return err
		}
		listed := make(map[string]bool, len(info.Domains))
		for _, d := range info.Domains {
			listed[d.Domain] = true
		}
		gone := make(map[string]bool, len(args))
		for _, a := range args {
			domain := strings.ToLower(strings.TrimSpace(a))
			if !listed[domain] {
				return notFoundf("domain %s is not listed, see `zbox workspace domain list`", domain)
			}
			gone[domain] = true
		}
		kept := make([]api.BaseWorkspaceDomain, 0, len(info.Domains))
		for _, d := range info.Domains {
			if !gone[d.Domain] {
				kept = append(kept, d)
			}
		}
		info, err = ws.PutWorkspaceDomains(cmd.Context(), kept)
		if err != nil {
			return err
		}
		return emit(info.Domains, func() {
			for _, d := range args {
				say("Domain %s removed.", strings.ToLower(strings.TrimSpace(d)))
			}
			printDomains(info.Domains)
		})
	},
}

func domainForm(flag string) (api.BaseWorkspaceDomainForm, error) {
	switch strings.ToLower(flag) {
	case "dot":
		return api.DOT, nil
	case "dash":
		return api.DASH, nil
	}
	return "", usagef("--form takes dot or dash, not %q", flag)
}

// domainExample is the host shape a sandbox port takes under the domain, the way the API builds it.
func domainExample(d api.BaseWorkspaceDomain) string {
	if d.Form == api.DASH {
		return "{port}-{id}-" + d.Domain
	}
	return "{port}-{id}." + d.Domain
}

func printDomains(domains []api.BaseWorkspaceDomain) {
	if len(domains) == 0 {
		note("No domain, so no sandbox port has a public URL. `zbox workspace domain add <domain>` adds one. The project network reaches a port at `http://<workspace>.zerops:8081/` with the Host header `{port}-{id}`.")
		return
	}
	tb := newTable("DOMAIN", "FORM", "SANDBOX PORT")
	for _, d := range domains {
		tb.row(d.Domain, lower(d.Form), domainExample(d))
	}
	tb.print()
}

var switchCmd = &cobra.Command{
	Use:   "switch [workspace]",
	Short: "Make another known workspace the current one",
	Long:  "The workspace is named by its name, API host (or a prefix of it) or service id. Without an argument the known workspaces are listed. Nothing is contacted, the key stored for it is used from the next command on.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return emit(withoutSecrets(c.Workspaces), func() { printWorkspaces(c) })
		}
		if err := refuseOverride("zbox workspace switch"); err != nil {
			return err
		}
		w, err := c.Lookup(args[0])
		if err != nil {
			return err
		}
		if err := c.Assign(w); err != nil {
			return err
		}
		return emit(w.WithoutSecrets(), func() { result(w.Name, "Switched to %s (%s).", w.Name, w.Label()) })
	},
}

func init() {
	workspacePolicyCmd.Flags().StringVarP(&flagPolicyFile, "file", "f", "", "JSON file with the new policy, - for stdin")
	workspaceUseCmd.Flags().StringVar(&flagWorkspaceKey, "api-key", "", "workspace API key, or a token from `zbox token create`")
	workspaceUseCmd.Flags().StringVar(&flagWorkspaceName, "name", "", "name for `zbox workspace switch`, defaults to the first label of the API host")
	workspaceAuthorizeCmd.Flags().StringVar(&flagWorkspaceName, "name", "", "name for `zbox workspace switch`, defaults to the service hostname")
	openCmd.Flags().StringVar(&flagOpenDomain, "domain", "", "open the port under this workspace domain instead of the first one")
	workspaceDomainAddCmd.Flags().StringVar(&flagDomainForm, "form", "dot", "dot for an own wildcard domain ({port}-{id}.<domain>), dash for a Zerops subdomain ({port}-{id}-<domain>)")
	workspaceDomainCmd.AddCommand(workspaceDomainLsCmd, workspaceDomainAddCmd, workspaceDomainRmCmd)
	waits(workspaceStartCmd, "return once Zerops took the start, without waiting for the process and the orchestrator")
	waits(workspaceStopCmd, "return once Zerops took the stop, without waiting for the process")
	waits(workspaceRestartCmd, "return once Zerops took the restart, without waiting for the process and the orchestrator")
	waits(workspaceReloadCmd, "return once Zerops took the reload, without waiting for the process and the orchestrator")
	waits(workspaceResizeCmd, "return once Zerops took the new size, without waiting for the process and the orchestrator")
	destructive(workspaceStopCmd, workspaceRestartCmd, workspaceResizeCmd)
	workspaceResizeCmd.Flags().IntVar(&flagResizeCpu, "cpu", 0, "cpu cores of the workspace VM (min 2)")
	workspaceResizeCmd.Flags().IntVar(&flagResizeRam, "ram", 0, "RAM of the workspace VM in GiB (min 2)")
	workspaceResizeCmd.Flags().IntVar(&flagResizeDisk, "disk", 0, "disk of the workspace VM in GiB (min 10), it only grows")
	workspaceResizeCmd.Flags().StringVar(&flagResizeCpuMode, "cpu-mode", "", cpuModeHelp+", the mode the workspace has when left out; the platform refuses a second change of the mode within its block window")
	workspaceCmd.AddCommand(workspaceLsCmd, workspaceAuthorizeCmd, workspaceKeyCmd, workspacePolicyCmd, workspaceDomainCmd, workspaceUseCmd, workspaceForgetCmd, switchCmd, consoleCmd, statusCmd,
		workspaceStartCmd, workspaceStopCmd, workspaceRestartCmd, workspaceReloadCmd, workspaceResizeCmd, teardownCmd)
	rootCmd.AddCommand(workspaceCmd, openCmd)
}

var openCmd = &cobra.Command{
	Use:   "open <sandbox-id|name> [port]",
	Short: "Open a port of a sandbox in the browser, signed in",
	Long:  "Opens one of the sandbox's HTTP ports (the only one when the port is left out): a members or ticket port gets a one-time ticket, a token port a token, both valid for 60 s and issued for this opening only, and the port URL opens with it; the router swaps it for a session cookie and redirects to the clean URL. A public port opens bare, a project port is not reachable through the edge. The URL is the port's first one, under the first workspace domain, unless --domain names another of them (`zbox workspace domain list`). Scripts talk to a token port with a token from `zbox cred issue` in the X-Zerops-Box-Authorization: Bearer header, members ports take no API key. Zerops Box (box.zerops.io) opens ports too, `zbox workspace console` opens the workspace there.",
	Example: `  zbox open app
  zbox open app 3000
  zbox open app 3000 --domain sb.example.com`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		return openPort(cmd, ws, args[0], args[1:])
	},
}

var consoleCmd = &cobra.Command{
	Use:   "console",
	Short: "Open the workspace in Zerops Box, the web console, in the browser",
	Long:  "Opens `https://box.zerops.io/workspace/<service-id>` in the browser and prints the URL (`{\"url\": ...}` under --json). Zerops Box signs the browser in with the Zerops login and reaches the workspace through Zerops, so nothing is minted or sent here. A workspace stored with `zbox workspace use --api-key` has no service id and no web console, it stays CLI only; `zbox workspace authorize <service-id>` stores a workspace of the account. A sandbox port is opened with `zbox open`.",
	Example: `  zbox workspace console
  zbox workspace console --workspace prod
  zbox workspace console --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadSelected()
		if err != nil {
			return err
		}
		w := c.EffectiveWorkspace()
		if !w.Usable() {
			return notAuthorized{noWorkspaceHint}
		}
		if w.Direct() {
			return usagef("workspace %s (%s) is reached by URL and key, not through the Zerops account, and has no web console: `zbox workspace authorize <service-id>` stores a workspace of the account, `zbox workspace list` lists them", w.Name, w.Label())
		}
		box, err := c.EffectiveBoxUrl()
		if err != nil {
			return usagef("%v; `zbox login --box-url <origin>` stores another one", err)
		}
		if box == config.DefaultBoxUrl && strings.TrimSuffix(c.ApiUrl, "/") != config.DefaultApiUrl {
			note("The API is %s but the console opens on the production Zerops Box (%s). `zbox login --box-url <origin>` stores the Box of that environment, --box-url sets it for one run.", c.ApiUrl, box)
		}
		return browse(consoleUrl(box, w.ServiceId), "Could not open a browser, open the URL above.")
	},
}

// boxHost is the host of the Zerops Box that `zbox workspace console` opens, for messages.
func boxHost(c config.Config) string {
	box, err := c.EffectiveBoxUrl()
	if err != nil {
		return c.BoxUrl
	}
	return strings.TrimPrefix(strings.TrimPrefix(box, "https://"), "http://")
}

// consoleUrl is the page of the workspace service in Zerops Box.
func consoleUrl(boxUrl, serviceId string) string {
	return boxUrl + "/workspace/" + url.PathEscape(serviceId)
}

// openTicketTtl is how long the credential minted for one `zbox open` lives: long enough to land in the browser, not long enough to be worth copying.
const openTicketTtl = 60

// openPort opens one HTTP port of a sandbox: the port named, or the only one with a URL. A members or ticket port is entered with a fresh one-time ticket, a token port with a fresh token (the router takes either once as a query parameter and sets the session cookie), a public port bare. The credential is minted here and now, listings never carry one.
func openPort(cmd *cobra.Command, ws *api.Workspace, id string, rest []string) error {
	s, err := ws.Sandbox(cmd.Context(), id)
	if err != nil {
		return err
	}
	var port api.BaseIngressPort
	switch len(rest) {
	case 1:
		n, err := strconv.Atoi(rest[0])
		if err != nil {
			return usagef("%q is not a port number", rest[0])
		}
		found := false
		for _, p := range declaredPorts(s) {
			if p.Port == n {
				port, found = p, true
			}
		}
		if !found {
			return notFoundf("sandbox %s has no port %d declared, see `zbox port list %s`", s.Id, n, s.Id)
		}
	default:
		var candidates []api.BaseIngressPort
		for _, p := range declaredPorts(s) {
			if portUrl(s, p.Port) != "" {
				candidates = append(candidates, p)
			}
		}
		switch len(candidates) {
		case 1:
			port = candidates[0]
		case 0:
			return notFoundf("sandbox %s has no HTTP port with a URL, open one with `zbox port open %s <port>`", s.Id, s.Id)
		default:
			ports := make([]string, 0, len(candidates))
			for _, p := range candidates {
				ports = append(ports, strconv.Itoa(p.Port))
			}
			return usagef("sandbox %s has several HTTP ports, name one: %s", s.Id, strings.Join(ports, ", "))
		}
	}
	base := portUrl(s, port.Port)
	if base == "" {
		return usagef("port %d of sandbox %s has no URL: %s ports are not reachable through the edge", port.Port, s.Id, lower(port.Access))
	}
	if flagOpenDomain != "" {
		if base = portUrlUnder(s, port.Port, flagOpenDomain); base == "" {
			return notFoundf("port %d of sandbox %s has no URL under %s, see `zbox workspace domain list`", port.Port, s.Id, flagOpenDomain)
		}
	}
	var kind api.RequestPostIngressCredentialKind
	switch port.Access {
	case api.BaseIngressPortAccessPUBLIC:
		return openInBrowser(base)
	case api.BaseIngressPortAccessTOKEN:
		kind = api.RequestPostIngressCredentialKindTOKEN
	default:
		kind = api.RequestPostIngressCredentialKindTICKET
	}
	issued, err := ws.IssueCredential(cmd.Context(), s.Id, api.RequestPostIngressCredential{Kind: kind, Port: port.Port, Name: optional("zbox open"), TtlSeconds: optionalInt(openTicketTtl)})
	if err != nil {
		return fmt.Errorf("issue a %s for port %d: %w", lower(kind), port.Port, err)
	}
	return openInBrowser(openUrl(base, port.Access, issued.Secret))
}

// openInBrowser opens a URL that carries a credential. The credential is single use and short lived, so a failed browser launch says to hurry.
func openInBrowser(target string) error {
	return browse(target, "Could not open a browser, open the URL within a minute, it is single use.")
}

// browse prints the URL ({"url": …} under --json), opens it, and notes failed when no browser starts.
func browse(target, failed string) error {
	if err := emit(map[string]string{"url": target}, func() { fmt.Println(target) }); err != nil {
		return err
	}
	if err := browser.Open(target); err != nil {
		note("%s", failed)
	}
	return nil
}

var workspaceKeyCmd = &cobra.Command{
	Use:   "key",
	Short: "Print the workspace API key, for `zbox workspace use --api-key` on a machine without the Zerops login",
	Long:  "The key is the generated service env ZBOX_WORKSPACE_API_KEY. For a workspace of the account it is read from the service through the public API in sudo mode, which the browser login enters through the consent page (a read-only role gets REDACTED and is refused); the CLI itself never stores or sends it, zproxy does. For a workspace pointed at by hand the stored key is printed. An edited key takes effect with `zbox workspace reload`.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadSelected()
		if err != nil {
			return err
		}
		w := c.EffectiveWorkspace()
		if !w.Usable() {
			return notAuthorized{noWorkspaceHint}
		}
		key := w.ApiKey
		if !w.Direct() {
			zc, err := zeropsClient(cmd.Context(), &c)
			if err != nil {
				return err
			}
			// A sensitive env is REDACTED outside sudo mode whatever the role.
			if err := ensureSudo(cmd, c, zc); err != nil {
				return err
			}
			value, found, err := zc.ServiceEnv(cmd.Context(), w.ServiceId, apiKeyEnv)
			if err != nil {
				return err
			}
			if !found || !keyReadable(value) {
				return notAuthorized{fmt.Sprintf("the %s env of workspace %s is not readable with this login (a read-only role gets it redacted), the Zerops GUI shows it to an admin", apiKeyEnv, w.Name)}
			}
			key = value
		}
		return emit(map[string]string{"apiKey": key}, func() { fmt.Println(key) })
	},
}

var (
	flagResizeCpu     int
	flagResizeRam     int
	flagResizeDisk    int
	flagResizeCpuMode string
)

// parkWarning is what a stop, a restart and a resize of a running workspace do to its sandboxes.
const parkWarning = "Every running sandbox is parked with its memory first, one that the 30 s shutdown budget does not reach comes back parked without memory."

const cpuModeHelp = "shared or dedicated cpu cores, dedicated cost several times more per core"

// cpuMode reads a --cpu-mode flag into the spelling of the platform, empty when not given.
func cpuMode(flag string) (string, error) {
	switch strings.ToLower(flag) {
	case "":
		return "", nil
	case "shared":
		return zerops.CpuShared, nil
	case "dedicated":
		return zerops.CpuDedicated, nil
	}
	return "", usagef("--cpu-mode takes shared or dedicated, not %q", flag)
}

var workspaceStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the stopped workspace VM",
	Long:  "Starts the Zerops service of the workspace and waits for the Zerops process, then until the orchestrator answers. The sandboxes stay parked as the stop left them, a request or `zbox wake` wakes one. A running workspace is left as it is and the command exits 0.",
	Example: `  zbox workspace start
  zbox workspace start --workspace prod --no-wait`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLifecycle(cmd, lifecycle{
			verb: "start", past: "started", ongoing: "starting", already: enum.ServiceStackStatusEnumActive, alreadyWord: "running", ping: true,
			call: (*zerops.Client).StartServiceStack,
		})
	},
}

var workspaceStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the workspace VM, its sandboxes are parked first",
	Long:  "Stops the Zerops service of the workspace and waits for the Zerops process. " + parkWarning + " Nothing answers until `zbox workspace start`. Asks first, --yes on a script. A stopped workspace is left as it is and the command exits 0.",
	Example: `  zbox workspace stop
  zbox workspace stop -y --workspace prod`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLifecycle(cmd, lifecycle{
			verb: "stop", past: "stopped", ongoing: "stopping", already: enum.ServiceStackStatusEnumStopped, alreadyWord: "stopped",
			warning: parkWarning + " Nothing answers until `zbox workspace start`.",
			call:    (*zerops.Client).StopServiceStack,
		})
	},
}

var workspaceRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the workspace VM, its sandboxes are parked first",
	Long:  "Restarts the Zerops service of the workspace, which reboots the VM, and waits for the Zerops process, then until the orchestrator answers. " + parkWarning + " Asks first, --yes on a script.",
	Example: `  zbox workspace restart
  zbox workspace restart -y --no-wait`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLifecycle(cmd, lifecycle{
			verb: "restart", past: "restarted", ongoing: "restarting", ping: true,
			warning: "The VM reboots. " + parkWarning,
			call:    (*zerops.Client).RestartServiceStack,
		})
	},
}

var workspaceReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Restart the orchestrator inside the workspace VM, the sandboxes keep running",
	Long: `Reloads the Zerops service of the workspace: the orchestrator is stopped, its configuration rewritten from the service, prepared and started
again inside the same VM, which does not reboot. Running sandboxes stay running and nothing is parked. This is how an edited service env
(a changed ZBOX_WORKSPACE_API_KEY, a connector credential edited in Zerops) or a new orchestrator build takes effect.
Waits for the Zerops process, then until the orchestrator answers. Does not ask, nothing is lost. Only a running workspace reloads.`,
	Example: `  zbox workspace reload
  zbox workspace reload --workspace prod --no-wait`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLifecycle(cmd, lifecycle{
			verb: "reload", past: "reloaded", ongoing: "reloading", requires: enum.ServiceStackStatusEnumActive, ping: true,
			call: (*zerops.Client).ReloadServiceStack,
		})
	},
}

// lifecycle is one of `zbox workspace start|stop|restart|reload`.
type lifecycle struct {
	verb, past, ongoing string
	// already is the service status the verb aims at, which makes the command a no-op. Empty for restart.
	already     enum.ServiceStackStatusEnum
	alreadyWord string
	// requires is the only service status the verb works on, empty for any.
	requires enum.ServiceStackStatusEnum
	// warning is the cost the confirmation names, empty for a verb that does not ask.
	warning string
	// ping waits for the orchestrator to answer after the process.
	ping bool
	call func(*zerops.Client, context.Context, string) (output.Process, error)
}

func runLifecycle(cmd *cobra.Command, l lifecycle) error {
	ctx := cmd.Context()
	c, w, zc, err := serviceWorkspace(ctx, l.verb)
	if err != nil {
		return err
	}
	stack, err := workspaceStack(ctx, zc, w)
	if err != nil {
		return err
	}
	if l.already != "" && stack.Status == l.already {
		return emitChange(w, stack, nil, func() { result(w.Name, "Workspace %s is already %s.", w.Name, l.alreadyWord) })
	}
	if err := requireStatus(w, stack.Status, l); err != nil {
		return err
	}
	if l.warning != "" {
		if err := confirm(fmt.Sprintf("%s workspace %s (%s)", l.verb, w.Name, w.ServiceId), l.warning); err != nil {
			return err
		}
	}
	began := time.Now()
	process, err := l.call(zc, ctx, w.ServiceId)
	if err != nil {
		return err
	}
	pid := process.Id.Native()
	if !flagNoWait {
		note("Workspace %s is %s, Zerops process %s.", w.Name, l.ongoing, pid)
	}
	if err := waitWorkspace(cmd, c, zc, w, pid, l.verb, l.ping); err != nil {
		return err
	}
	return finishChange(ctx, zc, w, &process, func(zerops.Size) {
		if flagNoWait {
			result(w.Name, "Workspace %s is %s, Zerops process %s.", w.Name, l.ongoing, pid)
			return
		}
		result(w.Name, "Workspace %s %s in %s.", w.Name, l.past, durationString(time.Since(began)))
		if l.verb == "stop" {
			note("`zbox workspace start` starts it again.")
		}
	})
}

// requireStatus refuses a verb on a workspace in a status it does not work on. The workspace will be reachable again, so the class is unavailable.
func requireStatus(w config.Workspace, status enum.ServiceStackStatusEnum, l lifecycle) error {
	switch {
	case l.requires == "" || status == l.requires:
		return nil
	case status == enum.ServiceStackStatusEnumStopped:
		return unavailable{fmt.Sprintf("workspace %s is stopped and only a running workspace can %s, `zbox workspace start` starts it", w.Name, l.verb)}
	}
	return unavailable{fmt.Sprintf("workspace %s is %s and only a running workspace can %s, `zbox workspace status` shows when it is", w.Name, lower(status), l.verb)}
}

var workspaceResizeCmd = &cobra.Command{
	Use:   "resize",
	Short: "Change the cpu cores, RAM or disk of the workspace VM",
	Long: `Sets the size of the workspace VM, each resource pinned as min and max: --cpu cores, --ram and --disk in GiB, --cpu-mode shared or dedicated.
What no flag names stays as it is, the cpu mode included. The floor is 2 cpu, 2 GiB RAM and 10 GiB disk, and the disk only grows.
After a cpu mode change the platform refuses a second one within its block window, and its answer says when the mode can change again.
A running workspace restarts at once to apply the change. ` + parkWarning + ` Asks first, --yes on a script.
Waits for the Zerops process and the orchestrator, then prints the size the platform applied: a size above the account's limit is trimmed to it.
A workspace that already has the size is left as it is and the command exits 0.`,
	Example: `  zbox workspace resize --ram 8
  zbox workspace resize --cpu 4 --ram 16 --disk 50 -y
  zbox workspace resize --cpu-mode dedicated --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := resizeRequest(flagResizeCpu, flagResizeRam, flagResizeDisk, flagResizeCpuMode)
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		c, w, zc, err := serviceWorkspace(ctx, "resize")
		if err != nil {
			return err
		}
		stack, err := workspaceStack(ctx, zc, w)
		if err != nil {
			return err
		}
		size := zerops.StackSize(stack)
		if zerops.Applied(stack, r) {
			return emitChange(w, stack, nil, func() {
				result(w.Name, "Workspace %s already has %s.", w.Name, sizeString(size))
				noteTrimmed(size, r)
			})
		}
		floor := zerops.DiskFloor(stack)
		if err := checkResize(floor, r); err != nil {
			return err
		}
		running := stack.Status == enum.ServiceStackStatusEnumActive
		grows := float64(r.Disk) > floor
		// A disk grow sent while the service is stopped reaches the API and the VM but not the data volume at the next start.
		if stack.Status == enum.ServiceStackStatusEnumStopped && grows {
			return unavailable{fmt.Sprintf("workspace %s is stopped and its disk grows only while it runs, `zbox workspace start` it first", w.Name)}
		}
		var warning []string
		if running {
			warning = append(warning, "The VM restarts at once. "+parkWarning)
		}
		if grows {
			warning = append(warning, "The disk cannot shrink back.")
		}
		if err := confirm(fmt.Sprintf("resize workspace %s (%s) to %s", w.Name, w.ServiceId, sizeString(resized(size, r))), strings.Join(warning, " ")); err != nil {
			return err
		}
		in, err := zerops.AutoscalingBody(stack, r)
		if err != nil {
			return err
		}
		began := time.Now()
		process, err := zc.Autoscale(ctx, w.ServiceId, in)
		if err != nil {
			return err
		}
		var pid string
		if process != nil {
			pid = process.Id.Native()
			if !flagNoWait {
				note("Workspace %s is resizing, Zerops process %s.", w.Name, pid)
			}
		}
		if err := waitWorkspace(cmd, c, zc, w, pid, "resize", running); err != nil {
			return err
		}
		return finishChange(ctx, zc, w, process, func(applied zerops.Size) {
			switch {
			case flagNoWait && pid != "":
				result(w.Name, "Workspace %s is resizing to %s, Zerops process %s.", w.Name, sizeString(applied), pid)
			default:
				result(w.Name, "Workspace %s resized to %s in %s.", w.Name, sizeString(applied), durationString(time.Since(began)))
			}
			noteTrimmed(applied, r)
		})
	},
}

func noteTrimmed(applied zerops.Size, r zerops.Resize) {
	if trimmed(applied, r) {
		note("The platform applies %s, not the size asked for: a size above the account's limit is trimmed to it.", sizeString(applied))
	}
}

// resizeRequest reads the flags of `zbox workspace resize`. A call without any of them is a usage error, so is a size under the floor.
func resizeRequest(cpu, ram, disk int, mode string) (zerops.Resize, error) {
	m, err := cpuMode(mode)
	if err != nil {
		return zerops.Resize{}, err
	}
	r := zerops.Resize{Cpu: cpu, Ram: ram, Disk: disk, CpuMode: m}
	if r == (zerops.Resize{}) {
		return r, usagef("give --cpu, --ram, --disk or --cpu-mode")
	}
	// The disk floor waits for checkResize, where a disk under the current one gets the more useful message.
	if err := zerops.CheckSize(cpu, ram, 0); err != nil {
		return r, usageError{err}
	}
	if disk < 0 {
		return r, usagef("--disk %d is not a size", disk)
	}
	return r, nil
}

// checkResize refuses a disk under diskFloor (zerops.DiskFloor) or under the size floor before the API is called, the platform cannot shrink the disk of a VM.
func checkResize(diskFloor float64, r zerops.Resize) error {
	if r.Disk > 0 && float64(r.Disk) < diskFloor {
		return usagef("the disk of a workspace only grows, it is %g GiB now and --disk %d is smaller", diskFloor, r.Disk)
	}
	if err := zerops.CheckSize(0, 0, r.Disk); err != nil {
		return usageError{err}
	}
	return nil
}

// resized is the size r asks for on top of the current one.
func resized(s zerops.Size, r zerops.Resize) zerops.Size {
	if r.CpuMode != "" {
		s.CpuMode = r.CpuMode
	}
	if r.Cpu > 0 {
		s.Cpu = r.Cpu
	}
	if r.Ram > 0 {
		s.Ram = float64(r.Ram)
	}
	if r.Disk > 0 {
		s.Disk = float64(r.Disk)
	}
	return s
}

// trimmed says whether the platform applied less than r asks for.
func trimmed(applied zerops.Size, r zerops.Resize) bool {
	return (r.Cpu > 0 && applied.Cpu != r.Cpu) || (r.Ram > 0 && applied.Ram != float64(r.Ram)) || (r.Disk > 0 && applied.Disk != float64(r.Disk)) ||
		(r.CpuMode != "" && applied.CpuMode != r.CpuMode)
}

// sizeString is a workspace size as the commands print it: `4 cpu (shared), 8 GiB RAM, 50 GiB disk`.
func sizeString(s zerops.Size) string {
	if !s.Known() {
		return "not named by the service"
	}
	cpu := fmt.Sprintf("%d cpu", s.Cpu)
	if s.CpuMode != "" {
		cpu += " (" + lower(s.CpuMode) + ")"
	}
	return fmt.Sprintf("%s, %g GiB RAM, %g GiB disk", cpu, s.Ram, s.Disk)
}

// serviceWorkspace is the workspace a lifecycle command acts on, which has to be a service of the account, with the client of the login.
func serviceWorkspace(ctx context.Context, verb string) (*config.Config, config.Workspace, *zerops.Client, error) {
	c, err := loadSelected()
	if err != nil {
		return nil, config.Workspace{}, nil, err
	}
	w := c.EffectiveWorkspace()
	if !w.Usable() {
		return nil, w, nil, notAuthorized{noWorkspaceHint}
	}
	if w.Direct() {
		return nil, w, nil, usagef("workspace %s (%s) is reached by URL and key, not through the Zerops account, so there is no service to %s: `zbox workspace authorize <service-id>` stores a workspace of the account, `zbox workspace list` lists them", w.Name, w.Label(), verb)
	}
	zc, err := zeropsClient(ctx, &c)
	if err != nil {
		return nil, w, nil, err
	}
	return &c, w, zc, nil
}

func workspaceStack(ctx context.Context, zc *zerops.Client, w config.Workspace) (output.ServiceStack, error) {
	stack, err := zc.ServiceStack(ctx, w.ServiceId)
	if err != nil && zerops.IsNotFound(err) {
		return stack, notFoundf("workspace %s (%s) is gone from the account, `zbox workspace forget` drops it here", w.Name, w.Label())
	}
	return stack, err
}

// waitWorkspace waits within --timeout, its only deadline, for the Zerops process of a lifecycle command and, with ping, until the orchestrator answers again. --no-wait skips it.
func waitWorkspace(cmd *cobra.Command, c *config.Config, zc *zerops.Client, w config.Workspace, processId, verb string, ping bool) error {
	if flagNoWait {
		return nil
	}
	ctx, cancel := waitContext(cmd.Context())
	defer cancel()
	if processId != "" {
		if err := zc.WaitProcesses(ctx, []string{processId}, func(zerops.ProcessState) {}); err != nil {
			return timedOut(err, fmt.Sprintf("the %s process %s still runs on Zerops", verb, processId), "`zbox workspace status` shows the service")
		}
	}
	if !ping {
		return nil
	}
	err := waitForAccess(ctx, c, zc, w, 0)
	if err == nil {
		var ws *api.Workspace
		if ws, err = connect(ctx, c); err == nil {
			err = waitForPing(ctx, ws, 0)
		}
	}
	// A call that the deadline cuts short returns its own error, which is still the timeout.
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return timedOut(err, fmt.Sprintf("the %s of workspace %s is done but the orchestrator has not answered yet", verb, w.Name), "`zbox workspace status` asks it again")
}

// workspaceChange is the --json answer of `zbox workspace start|stop|restart|reload|resize`: the service after the command, with the Zerops process
// as it ended (as the API answered it under --no-wait), null when there was nothing to do.
type workspaceChange struct {
	ServiceId string          `json:"serviceId"`
	Status    string          `json:"status"`
	Size      zerops.Size     `json:"size"`
	Process   *output.Process `json:"process"`
}

func emitChange(w config.Workspace, stack output.ServiceStack, process *output.Process, text func()) error {
	return emit(workspaceChange{ServiceId: w.ServiceId, Status: stack.Status.Native(), Size: zerops.StackSize(stack), Process: process}, text)
}

// finishChange reads the service again and prints the result, text gets the size the platform applies now.
func finishChange(ctx context.Context, zc *zerops.Client, w config.Workspace, process *output.Process, text func(zerops.Size)) error {
	stack, err := workspaceStack(ctx, zc, w)
	if err != nil {
		return err
	}
	if flagJson && process != nil && !flagNoWait {
		ended, err := zc.ProcessOutput(ctx, process.Id.Native())
		if err != nil {
			return err
		}
		process = &ended
	}
	return emitChange(w, stack, process, func() { text(zerops.StackSize(stack)) })
}
