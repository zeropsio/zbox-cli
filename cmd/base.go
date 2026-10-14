package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagBaseCredentialEnv string
	flagBaseName          string
	flagBuildVerbose      bool
	flagBuildDryRun       bool
	flagTemplateBase      string
	flagTemplateName      string
	flagTemplateWarm      bool
	flagTemplateWarmup    string
	flagTemplateMemory    string
	flagTemplateCpus      int
	flagTemplatePolicy    string
)

var baseCmd = &cobra.Command{
	Use:   "base",
	Short: "Manage bases, the images sandboxes boot from",
}

var baseAddCmd = &cobra.Command{
	Use:     "add <image-ref>",
	Short:   "Pull an OCI image and flatten it into a base",
	Example: `  zbox base add ubuntu:26.04 --name ubuntu`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		op, err := ws.CreateBase(cmd.Context(), api.RequestPostBase{ImageRef: args[0], RegistryCredentialEnv: optional(flagBaseCredentialEnv), Name: optional(flagBaseName)})
		if err != nil {
			return err
		}
		return finishBase(cmd, ws, op, "Pulling")
	},
}

var baseBuildCmd = &cobra.Command{
	Use:   "build [dir|archive]",
	Short: "Build a base from a Dockerfile on the workspace",
	Long: `Packs the directory (the current one by default) into a tar.gz, or sends a ready .tar, .tar.gz or .zip, and the orchestrator runs its Dockerfile
through BuildKit and registers the image as a base. The Dockerfile sits at the root of the context. What stays out is what docker build leaves out:
the root .dockerignore when there is one, the tree's .gitignore files otherwise, .git always; the Dockerfile and the .dockerignore are always sent.
One line says how much was packed and what governed it, --verbose lists every member, --dry-run only lists. A name given with --name is taken
over from the base that held it, so a rebuild replaces the earlier build for 'zbox new --base <name>'. The layer cache stays on the workspace
until 'zbox base prune'.`,
	Example: `  zbox base build ./image --name app
  zbox base build --dry-run`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		source := "."
		if len(args) == 1 {
			source = args[0]
		}
		body, size, report, err := buildContext(source)
		if err != nil {
			return err
		}
		defer body.Close()
		report.print(cmd.ErrOrStderr(), flagBuildVerbose || flagBuildDryRun)
		if flagBuildDryRun {
			return nil
		}
		op, err := ws.BuildBase(cmd.Context(), flagBaseName, body, size)
		if err != nil {
			return err
		}
		return finishBase(cmd, ws, op, "Building")
	},
}

var baseSetCmd = &cobra.Command{
	Use:   "set <base>",
	Short: "Rename a base, --name '' drops the name",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("name") {
			return usagef("nothing to set, give --name")
		}
		b, err := findBase(cmd, ws, args[0])
		if err != nil {
			return err
		}
		b, err = ws.UpdateBase(cmd.Context(), b.Digest, api.RequestPutBase{Name: optional(flagBaseName)})
		if err != nil {
			return err
		}
		return emit(b, func() { printBase(b) })
	},
}

var baseRenameCmd = &cobra.Command{
	Use:     "rename <base> <name>",
	Aliases: []string{"mv"},
	Short:   "Rename a base",
	Long:    "The name is refused while another base holds it (`nameConflict`). `zbox base set --name ''` drops a name.",
	Example: `  zbox base rename da2f61de1653 ubuntu
  zbox base mv ubuntu ubuntu-old`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		b, err := findBase(cmd, ws, args[0])
		if err != nil {
			return err
		}
		b, err = ws.UpdateBase(cmd.Context(), b.Digest, api.RequestPutBase{Name: &args[1]})
		if err != nil {
			return err
		}
		return emit(b, func() { printBase(b) })
	},
}

var basePruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove the whole layer cache of the Dockerfile builds",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		cache, err := ws.BuildCache(cmd.Context())
		if err != nil {
			return err
		}
		if cache.SizeBytes == 0 {
			say("No build cache.")
			return nil
		}
		if err := confirm(fmt.Sprintf("remove the build cache (%s)", bytesString(cache.SizeBytes)), "The next build of every Dockerfile starts cold."); err != nil {
			return err
		}
		if err := ws.PruneBuildCache(cmd.Context()); err != nil {
			return err
		}
		say("Build cache removed, %s freed.", bytesString(cache.SizeBytes))
		return nil
	},
}

var baseLsCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List bases",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Bases(cmd.Context())
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("DIGEST", "NAME", "IMAGE", "STATUS", "SIZE", "EXPOSES", "CREATED")
			for _, b := range list {
				tb.row(short(b.Digest), dash(str(b.Name)), b.ImageRef, b.Status, bytesPtr(b.SizeBytes), ints(b.ExposePorts), when(b.Created))
			}
			tb.ifEmpty("No bases. `zbox base add <image>` pulls one, `zbox base build` builds one from a Dockerfile.")
			tb.print()
		})
	},
}

var baseGetCmd = &cobra.Command{
	Use:   "get <digest>",
	Short: "Show one base",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		b, err := findBase(cmd, ws, args[0])
		if err != nil {
			return err
		}
		return emit(b, func() { printBase(b) })
	},
}

var baseRmCmd = &cobra.Command{
	Use:     "remove <digest>...",
	Aliases: []string{"rm"},
	Short:   "Delete bases nothing references any more",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		var bases []*api.BaseBase
		var labels []string
		for _, ref := range args {
			b, err := findBase(cmd, ws, ref)
			if err != nil {
				return err
			}
			bases = append(bases, b)
			labels = append(labels, named(b.Name, b.Digest))
		}
		return deleteEach("delete base "+strings.Join(labels, ", "), "", labels, "deleted", func(i int) (*api.BaseOperation, error) {
			return nil, ws.DeleteBase(cmd.Context(), bases[i].Digest)
		})
	},
}

func printBase(b *api.BaseBase, extra ...string) {
	pairs := []string{"Digest", short(b.Digest), "Name", dash(str(b.Name)), "Image", b.ImageRef, "Status", string(b.Status)}
	if b.FailedReason != nil {
		pairs = append(pairs, "Reason", *b.FailedReason)
	}
	pairs = append(pairs, "Size", bytesPtr(b.SizeBytes), "Exposes", dash(ints(b.ExposePorts)), "Created", when(b.Created))
	if b.KernelDigest != nil {
		pairs = append(pairs, "Kernel", short(*b.KernelDigest))
	}
	fields(append(pairs, extra...)...)
	if len(b.Labels) > 0 || b.LaunchContract.Cmd != nil || b.LaunchContract.Entrypoint != nil {
		section("Launch", b.LaunchContract)
	}
}

func ints(ns []int) string {
	if len(ns) == 0 {
		return "-"
	}
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = strconv.Itoa(n)
	}
	return strings.Join(out, ",")
}

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage templates, bases with a policy and optionally a warm memory snapshot",
}

var templateCreateCmd = &cobra.Command{
	Use:     "create",
	Short:   "Create a template from a base",
	Example: `  zbox template create --base app --name dev --warm --cpus 2 --memory 4g --warmup 'claude --version'`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		b, err := findBase(cmd, ws, flagTemplateBase)
		if err != nil {
			return err
		}
		req := api.RequestPostTemplate{BaseDigest: b.Digest, Name: optional(flagTemplateName)}
		if flagTemplateWarm {
			req.Warm = &flagTemplateWarm
		}
		// The warm-up runs in the build guest before the memory is captured, so it only has an effect on a warm template.
		if flagTemplateWarmup != "" && !flagTemplateWarm {
			return usagef("--warmup runs before the memory image is taken, it needs --warm")
		}
		req.Warmup = optional(flagTemplateWarmup)
		// The size is the one the memory image is taken at and the default of every sandbox spawned from the template; a spawn that names another size cold boots from the base instead.
		req.Cpus = optionalInt(flagTemplateCpus)
		if flagTemplateMemory != "" {
			n, err := parseBytes(flagTemplateMemory)
			if err != nil {
				return err
			}
			req.MemoryBytes = &n
		}
		if flagTemplatePolicy != "" {
			req.Policy = &api.BaseTemplatePolicy{}
			if err := readJson(flagTemplatePolicy, req.Policy); err != nil {
				return err
			}
		}
		t, err := ws.CreateTemplate(cmd.Context(), req)
		if err != nil {
			return err
		}
		if !flagNoWait {
			digest := short(t.Digest)
			note("Building template %s, a warm one takes about 20 s.", digest)
			ctx, cancel := waitContext(cmd.Context())
			defer cancel()
			// The wait answers no template when its own request fails, the digest is kept aside for the message.
			if t, err = waitTemplate(ctx, ws, t.Digest); err != nil {
				return timedOut(err, "template "+digest+" is still building", "`zbox template get "+digest+"` shows it")
			}
		}
		return emit(t, func() { printTemplate(t) })
	},
}

var templateLsCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List templates",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		list, err := ws.Templates(cmd.Context())
		if err != nil {
			return err
		}
		return emit(list, func() {
			tb := newTable("DIGEST", "NAME", "BASE", "STATUS", "WARM", "SIZE", "WARMUP", "CREATED")
			for _, t := range list {
				tb.row(short(t.Digest), dash(str(t.Name)), short(t.BaseDigest), t.Status, yesNo(t.HasMemory), templateSize(t), dash(str(t.Warmup)), when(t.Created))
			}
			tb.ifEmpty("No templates. `zbox template create --base <base> --warm` builds one.")
			tb.print()
		})
	},
}

var templateGetCmd = &cobra.Command{
	Use:   "get <digest|name>",
	Short: "Show one template with its policy",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		t, err := findTemplate(cmd, ws, args[0])
		if err != nil {
			return err
		}
		return emit(t, func() { printTemplate(t) })
	},
}

var templateSetCmd = &cobra.Command{
	Use:   "set <digest|name>",
	Short: "Rename a template or replace its policy",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		t, err := findTemplate(cmd, ws, args[0])
		if err != nil {
			return err
		}
		req := api.RequestPutTemplate{Name: optional(flagTemplateName)}
		if flagTemplatePolicy != "" {
			req.Policy = &api.BaseTemplatePolicy{}
			if err := readJson(flagTemplatePolicy, req.Policy); err != nil {
				return err
			}
		}
		t, err = ws.UpdateTemplate(cmd.Context(), t.Digest, req)
		if err != nil {
			return err
		}
		return emit(t, func() { printTemplate(t) })
	},
}

var templateRenameCmd = &cobra.Command{
	Use:     "rename <digest|name> <name>",
	Aliases: []string{"mv"},
	Short:   "Rename a template",
	Example: `  zbox template rename dev dev-old
  zbox template mv dev dev-old`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		t, err := findTemplate(cmd, ws, args[0])
		if err != nil {
			return err
		}
		t, err = ws.UpdateTemplate(cmd.Context(), t.Digest, api.RequestPutTemplate{Name: &args[1]})
		if err != nil {
			return err
		}
		return emit(t, func() { printTemplate(t) })
	},
}

var templateRmCmd = &cobra.Command{
	Use:     "remove <digest|name>...",
	Aliases: []string{"rm"},
	Short:   "Delete templates",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		var templates []*api.BaseTemplate
		var labels []string
		for _, ref := range args {
			t, err := findTemplate(cmd, ws, ref)
			if err != nil {
				return err
			}
			templates = append(templates, t)
			labels = append(labels, named(t.Name, t.Digest))
		}
		return deleteEach("delete template "+strings.Join(labels, ", "), "", labels, "deleted", func(i int) (*api.BaseOperation, error) {
			return nil, ws.DeleteTemplate(cmd.Context(), templates[i].Digest)
		})
	},
}

// waitTemplate holds until the template is READY, or fails with the orchestrator's reason when the build does. A warm-up has a 10 minute limit on the orchestrator side, so there is no timeout here.
func waitTemplate(ctx context.Context, ws *api.Workspace, digest string) (*api.BaseTemplate, error) {
	for {
		t, err := ws.Template(ctx, digest)
		if err != nil {
			return nil, err
		}
		switch t.Status {
		case api.BaseTemplateStatusREADY:
			return t, nil
		case api.BaseTemplateStatusDELETED:
			return t, fmt.Errorf("template %s was deleted", short(digest))
		case api.BaseTemplateStatusFAILED:
			return t, fmt.Errorf("template %s failed: %s", short(digest), dash(str(t.FailedReason)))
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func printTemplate(t *api.BaseTemplate) {
	pairs := []string{"Digest", short(t.Digest), "Name", dash(str(t.Name)), "Base", short(t.BaseDigest), "Status", string(t.Status)}
	if t.FailedReason != nil {
		pairs = append(pairs, "Reason", *t.FailedReason)
	}
	pairs = append(pairs, "Warm", yesNo(t.HasMemory), "Size", templateSize(*t), "Warm-up", dash(str(t.Warmup)), "Created", when(t.Created))
	fields(pairs...)
	section("Policy", t.Policy)
	if t.HasMemory {
		note("A sandbox spawned at this size resumes from the memory image, another size cold boots from the base.")
	}
}

// templateSize is the cpu count and memory the template's memory image is taken at, the default of its spawns.
func templateSize(t api.BaseTemplate) string {
	return fmt.Sprintf("%d cpu, %s", t.Cpus, bytesString(t.MemoryBytes))
}

func init() {
	baseAddCmd.Flags().StringVar(&flagBaseCredentialEnv, "registry-credential-env", "", "workspace env holding the credentials of a private registry")
	waits(baseAddCmd, "return right after the pull started, with its operation")
	baseAddCmd.Flags().StringVar(&flagBaseName, "name", "", "name the base takes, moved from the base that held it")
	baseBuildCmd.Flags().StringVar(&flagBaseName, "name", "", "name the built base takes, moved from the base that held it")
	waits(baseBuildCmd, "return right after the build started, with its operation")
	baseBuildCmd.Flags().BoolVarP(&flagBuildVerbose, "verbose", "v", false, "list every packed member and every file or tree left out")
	baseBuildCmd.Flags().BoolVar(&flagBuildDryRun, "dry-run", false, "pack and list, upload nothing")
	baseSetCmd.Flags().StringVar(&flagBaseName, "name", "", "new name, refused while another base holds it")
	baseCmd.AddCommand(baseAddCmd, baseBuildCmd, baseLsCmd, baseGetCmd, baseSetCmd, baseRenameCmd, basePruneCmd, baseRmCmd)

	templateCreateCmd.Flags().StringVar(&flagTemplateBase, "base", "", "base the template is built on: digest, prefix or image ref")
	templateCreateCmd.Flags().StringVar(&flagTemplateName, "name", "", "template name")
	templateCreateCmd.Flags().BoolVar(&flagTemplateWarm, "warm", false, "boot the base once and snapshot its memory so spawns resume instead of cold booting")
	templateCreateCmd.Flags().StringVar(&flagTemplateWarmup, "warmup", "", "shell command run in the guest before its memory is captured, so what it loads is resident in every spawn, for example 'claude --version'; needs --warm, a non-zero exit fails the build")
	templateCreateCmd.Flags().StringVar(&flagTemplateMemory, "memory", "", "memory the image is taken at and the default of spawns, for example 1g; the workspace default when omitted")
	templateCreateCmd.Flags().IntVar(&flagTemplateCpus, "cpus", 0, "cpu count of the image and the default of spawns")
	templateCreateCmd.Flags().StringVarP(&flagTemplatePolicy, "policy", "f", "", "JSON file with the template policy, - for stdin")
	waits(templateCreateCmd, "return right after the build started")
	templateSetCmd.Flags().StringVar(&flagTemplateName, "name", "", "new name")
	templateSetCmd.Flags().StringVarP(&flagTemplatePolicy, "policy", "f", "", "JSON file with the new template policy, - for stdin")
	templateCmd.AddCommand(templateCreateCmd, templateLsCmd, templateGetCmd, templateSetCmd, templateRenameCmd, templateRmCmd)
	destructive(baseRmCmd, basePruneCmd, templateRmCmd)
	rootCmd.AddCommand(baseCmd, templateCmd)
}

// finishBase waits for a pull or a build unless --no-wait and prints the base it produced.
func finishBase(cmd *cobra.Command, ws *api.Workspace, op *api.BaseOperation, doing string) error {
	if flagNoWait {
		return emit(op, func() { printOperation(op) })
	}
	note("%s, operation %s.", doing, op.Id)
	op, err := waitFor(cmd, ws, op)
	if err != nil {
		return err
	}
	b, err := ws.BaseOf(cmd.Context(), op)
	if err != nil {
		return err
	}
	return emit(b, func() { printBase(b, "Took", took(op.Created, op.Finished)) })
}
