package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

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
	Long:    "Without a subcommand the current workspace is described: API, domains, version, capacity, what is in use, the host facts and the policy. The subcommands manage the workspaces this machine knows (list, switch, authorize, use, forget), the current one (status, policy, domain, key, console) and its life (teardown); `zbox setup` creates one.",
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
	Short: "Point the CLI at a workspace by its API URL and key, outside the Zerops account",
	Long:  "The escape hatch for a workspace the Zerops login does not reach: a script with the key of its own workspace, another account's service. The orchestrator is called at the URL with the key as the bearer, nothing goes through zproxy. A workspace of the account is stored with `zbox workspace authorize <service-id>` instead and needs no key.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagWorkspaceKey == "" {
			return usagef("--api-key is required, it is the ZBOX_WORKSPACE_API_KEY env of the workspace service")
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
	Long:    "Every Box Workspace service (zbox@1) of the organisation is a workspace. A row with a name is stored here and can be switched to; the others need `zbox workspace authorize <service-id>` first, which stores them without any secret. Workspaces assigned with `zbox workspace use` that are not in the account are listed after.",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		zc, err := zeropsClient(cmd.Context(), &c)
		if err != nil {
			return emit(c.Workspaces, func() {
				printWorkspaces(c)
				note("Not logged in, only the workspaces known here are listed. Run `zbox login` to see the account's.")
			})
		}
		if c.ClientId == "" {
			user, err := zc.UserInfo(cmd.Context())
			if err != nil {
				return err
			}
			c.ClientId = user.ClientId
		}
		stacks, err := zc.Workspaces(cmd.Context(), c.ClientId)
		if err != nil {
			return err
		}
		return emit(map[string]any{"account": stacks, "known": c.Workspaces}, func() {
			byService := map[string]config.Workspace{}
			for _, w := range c.Workspaces {
				if w.ServiceId != "" {
					byService[w.ServiceId] = w
				}
			}
			listed := map[string]bool{}
			tb := newTable("", "SERVICE ID", "NAME", "HOSTNAME", "PROJECT", "STATUS", "STORED").keyCol(1)
			for _, st := range stacks {
				known, ok := byService[st.ServiceId]
				listed[st.ServiceId] = true
				tb.row(mark(c, known), st.ServiceId, dash(known.Name), st.Hostname, st.ProjectName, st.Status, map[bool]string{true: "yes", false: "-"}[ok])
			}
			for _, ws := range c.Workspaces {
				if ws.ServiceId != "" && listed[ws.ServiceId] {
					continue
				}
				tb.row(mark(c, ws), dash(ws.ServiceId), ws.Name, dash(ws.Host()), dash(ws.ProjectName), "not in this account", "yes")
			}
			tb.ifEmpty("No workspaces in the account. `zbox setup` creates one.")
			tb.print()
			printMarks(c)
		})
	},
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
	Long:  "A sandbox port gets one public URL per domain of the workspace, in list order; the first one is what `zbox open` and the console open. The edge routing is Zerops's side: the dash form `{port}-{id}-<domain>` is for the Zerops subdomain of the ingress port (`zbox setup` adds it when subdomain access is on), the dot form `{port}-{id}.<domain>` for an own wildcard domain, `*.<domain>`, routed to the ingress port 8081 in the Zerops GUI with its DNS records (A, AAAA and the ACME CNAME the GUI shows).",
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
		note("No domain, so no sandbox port has a public URL. `zbox workspace domain add <domain>` adds one.")
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
			return emit(c.Workspaces, func() { printWorkspaces(c) })
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
		return emit(w, func() { result(w.Name, "Switched to %s (%s).", w.Name, w.Label()) })
	},
}

func init() {
	workspacePolicyCmd.Flags().StringVarP(&flagPolicyFile, "file", "f", "", "JSON file with the new policy, - for stdin")
	workspaceUseCmd.Flags().StringVar(&flagWorkspaceKey, "api-key", "", "workspace API key")
	workspaceUseCmd.Flags().StringVar(&flagWorkspaceName, "name", "", "name for `zbox workspace switch`, defaults to the first label of the API host")
	workspaceAuthorizeCmd.Flags().StringVar(&flagWorkspaceName, "name", "", "name for `zbox workspace switch`, defaults to the service hostname")
	openCmd.Flags().StringVar(&flagOpenDomain, "domain", "", "open the port under this workspace domain instead of the first one")
	workspaceDomainAddCmd.Flags().StringVar(&flagDomainForm, "form", "dot", "dot for an own wildcard domain ({port}-{id}.<domain>), dash for a Zerops subdomain ({port}-{id}-<domain>)")
	workspaceDomainCmd.AddCommand(workspaceDomainLsCmd, workspaceDomainAddCmd, workspaceDomainRmCmd)
	workspaceCmd.AddCommand(workspaceLsCmd, workspaceAuthorizeCmd, workspaceKeyCmd, workspacePolicyCmd, workspaceDomainCmd, workspaceUseCmd, workspaceForgetCmd, switchCmd, consoleCmd, statusCmd, teardownCmd)
	rootCmd.AddCommand(workspaceCmd, openCmd)
}

var openCmd = &cobra.Command{
	Use:   "open <sandbox-id|name> [port]",
	Short: "Open a port of a sandbox in the browser, signed in",
	Long:  "Opens one of the sandbox's HTTP ports (the only one when the port is left out): a members or ticket port gets a one-time ticket, a token port a token, both valid for 60 s and issued for this opening only, and the port URL opens with it; the router swaps it for a session cookie and redirects to the clean URL. A public port opens bare, a project port is not reachable through the edge. The URL is the port's first one, under the first workspace domain, unless --domain names another of them (`zbox workspace domain list`). Scripts talk to a token port with a token from `zbox cred issue`, members ports take no API key. The console is `zbox workspace console`.",
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
	Short: "Open the console of the workspace in the browser, signed in",
	Long:  "The orchestrator serves the console at the root of its API. For a workspace of the account the command mints a fresh zproxy access for the browser (its own, not the CLI's, since the browser form carries the token in the URL) and opens it, `https://proxy.<domain>/service-ui/<token>/login`, where zproxy signs the browser in with the workspace key. For a workspace pointed at by hand it mints a single-use sign-in ticket with the key (`POST /console/login-ticket`, valid for 60 s) and opens `/console/login?ticket=` on the API host. A sandbox port is opened with `zbox open`.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadSelected()
		if err != nil {
			return err
		}
		if w := c.EffectiveWorkspace(); w.Usable() && !w.Direct() {
			zc, err := zeropsClient(cmd.Context(), &c)
			if err != nil {
				return err
			}
			a, err := zc.UiAccess(cmd.Context(), w.ServiceId)
			if err != nil {
				return fmt.Errorf("mint the console access: %w", err)
			}
			return openInBrowser(a.UrlUi)
		}
		ws, err := connect(cmd.Context(), &c)
		if err != nil {
			return err
		}
		ticket, err := ws.ConsoleLoginTicket(cmd.Context())
		if err != nil {
			var e api.ApiError
			if errors.As(err, &e) && e.Status == 404 {
				return fmt.Errorf("the workspace runs an older orchestrator without the console ticket route; restart the service to pick up the current one, or paste the key from `zbox workspace key` into the console's login form at %s", ws.BaseUrl)
			}
			return fmt.Errorf("mint a console ticket: %w", err)
		}
		return openInBrowser(ws.BaseUrl + "/console/login?ticket=" + url.QueryEscape(ticket.Ticket))
	},
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

// openInBrowser prints the URL ({"url": …} under --json) and opens it. The credential in it is single use and short lived, so a failed browser launch says to hurry.
func openInBrowser(target string) error {
	if err := emit(map[string]string{"url": target}, func() { fmt.Println(target) }); err != nil {
		return err
	}
	if err := browser.Open(target); err != nil {
		note("Could not open a browser, open the URL within a minute, it is single use.")
	}
	return nil
}

var workspaceKeyCmd = &cobra.Command{
	Use:   "key",
	Short: "Print the workspace API key, for `zbox workspace use --api-key` on a machine without the Zerops login",
	Long:  "The key is the generated service env ZBOX_WORKSPACE_API_KEY. For a workspace of the account it is read from the service through the public API in sudo mode, which the browser login enters through the consent page (a read-only role gets REDACTED and is refused); the CLI itself never stores or sends it, zproxy does. For a workspace pointed at by hand the stored key is printed.",
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
