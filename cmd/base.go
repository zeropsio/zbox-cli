package cmd

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagBaseCredentialEnv string
	flagTemplateBase      string
	flagTemplateName      string
	flagTemplateWarm      bool
	flagTemplateMemory    string
	flagTemplateCpus      int
	flagTemplatePolicy    string
)

var baseCmd = &cobra.Command{
	Use:   "base",
	Short: "Manage bases, the images sandboxes boot from",
}

var baseAddCmd = &cobra.Command{
	Use:   "add <image-ref>",
	Short: "Pull an OCI image and flatten it into a base",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		b, err := ws.CreateBase(cmd.Context(), api.RequestPostBase{ImageRef: args[0], RegistryCredentialEnv: optional(flagBaseCredentialEnv)})
		if err != nil {
			return err
		}
		if !flagNoWait {
			if b, err = waitForBase(cmd.Context(), ws, b.Digest, setupBaseTimeout); err != nil {
				return err
			}
		}
		return emit(b, func() { printBase(b) })
	},
}

var baseLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List bases",
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
			table("DIGEST\tIMAGE\tSTATUS\tSIZE\tEXPOSES\tAGE", func(w io.Writer) {
				for _, b := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", short(b.Digest), b.ImageRef, b.Status, bytesPtr(b.SizeBytes), ints(b.ExposePorts), age(b.Created))
				}
			})
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
	Use:   "rm <digest>...",
	Short: "Delete bases nothing references any more",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		for _, ref := range args {
			b, err := findBase(cmd, ws, ref)
			if err != nil {
				return err
			}
			if err := ws.DeleteBase(cmd.Context(), b.Digest); err != nil {
				return err
			}
			fmt.Println("deleted", short(b.Digest))
		}
		return nil
	},
}

func printBase(b *api.BaseBase) {
	pairs := []string{"Digest", b.Digest, "Image", b.ImageRef, "Status", string(b.Status)}
	if b.FailedReason != nil {
		pairs = append(pairs, "Reason", *b.FailedReason)
	}
	pairs = append(pairs, "Size", bytesPtr(b.SizeBytes), "Exposes", dash(ints(b.ExposePorts)), "Created", when(b.Created))
	if b.KernelDigest != nil {
		pairs = append(pairs, "Kernel", short(*b.KernelDigest))
	}
	fields(pairs...)
	if len(b.Labels) > 0 || b.LaunchContract.Cmd != nil || b.LaunchContract.Entrypoint != nil {
		fmt.Println("Launch:")
		_ = printJson(b.LaunchContract)
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
	Use:   "create",
	Short: "Create a template from a base",
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
			if t, err = waitForTemplate(cmd, ws, t.Digest); err != nil {
				return err
			}
		}
		return emit(t, func() { printTemplate(t) })
	},
}

var templateLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List templates",
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
			table("DIGEST\tNAME\tBASE\tSTATUS\tWARM\tSIZE\tAGE", func(w io.Writer) {
				for _, t := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%s\t%s\n", short(t.Digest), dash(str(t.Name)), short(t.BaseDigest), t.Status, t.HasMemory, templateSize(t), age(t.Created))
				}
			})
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

var templateRmCmd = &cobra.Command{
	Use:   "rm <digest|name>...",
	Short: "Delete templates",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		for _, ref := range args {
			t, err := findTemplate(cmd, ws, ref)
			if err != nil {
				return err
			}
			if err := ws.DeleteTemplate(cmd.Context(), t.Digest); err != nil {
				return err
			}
			fmt.Println("deleted", short(t.Digest))
		}
		return nil
	},
}

func waitForTemplate(cmd *cobra.Command, ws *api.Workspace, digest string) (*api.BaseTemplate, error) {
	for {
		t, err := ws.Template(cmd.Context(), digest)
		if err != nil {
			return nil, err
		}
		switch t.Status {
		case api.BaseTemplateStatusREADY:
			return t, nil
		case api.BaseTemplateStatusFAILED:
			return t, fmt.Errorf("template %s failed: %s", short(digest), dash(str(t.FailedReason)))
		}
		if err := sleep(cmd, 3); err != nil {
			return t, err
		}
	}
}

func printTemplate(t *api.BaseTemplate) {
	pairs := []string{"Digest", t.Digest, "Name", dash(str(t.Name)), "Base", short(t.BaseDigest), "Status", string(t.Status)}
	if t.FailedReason != nil {
		pairs = append(pairs, "Reason", *t.FailedReason)
	}
	pairs = append(pairs, "Warm", strconv.FormatBool(t.HasMemory), "Size", templateSize(*t), "Created", when(t.Created))
	fields(pairs...)
	if t.HasMemory {
		fmt.Println("A sandbox spawned at this size resumes from the memory image, another size cold boots from the base.")
	}
	fmt.Println("Policy:")
	_ = printJson(t.Policy)
}

// templateSize is the cpu count and memory the template's memory image is taken at, the default of its spawns.
func templateSize(t api.BaseTemplate) string {
	return fmt.Sprintf("%d cpu, %s", t.Cpus, bytesString(t.MemoryBytes))
}

func init() {
	baseAddCmd.Flags().StringVar(&flagBaseCredentialEnv, "registry-credential-env", "", "workspace env holding the credentials of a private registry")
	baseAddCmd.Flags().BoolVar(&flagNoWait, "no-wait", false, "return right after the pull started")
	baseCmd.AddCommand(baseAddCmd, baseLsCmd, baseGetCmd, baseRmCmd)

	templateCreateCmd.Flags().StringVar(&flagTemplateBase, "base", "", "base the template is built on: digest, prefix or image ref")
	templateCreateCmd.Flags().StringVar(&flagTemplateName, "name", "", "template name")
	templateCreateCmd.Flags().BoolVar(&flagTemplateWarm, "warm", false, "boot the base once and snapshot its memory so spawns resume instead of cold booting")
	templateCreateCmd.Flags().StringVar(&flagTemplateMemory, "memory", "", "memory the image is taken at and the default of spawns, for example 1g; the workspace default when omitted")
	templateCreateCmd.Flags().IntVar(&flagTemplateCpus, "cpus", 0, "cpu count of the image and the default of spawns")
	templateCreateCmd.Flags().StringVarP(&flagTemplatePolicy, "policy", "f", "", "JSON file with the template policy, - for stdin")
	templateCreateCmd.Flags().BoolVar(&flagNoWait, "no-wait", false, "return right after the build started")
	templateSetCmd.Flags().StringVar(&flagTemplateName, "name", "", "new name")
	templateSetCmd.Flags().StringVarP(&flagTemplatePolicy, "policy", "f", "", "JSON file with the new template policy, - for stdin")
	templateCmd.AddCommand(templateCreateCmd, templateLsCmd, templateGetCmd, templateSetCmd, templateRmCmd)
	rootCmd.AddCommand(baseCmd, templateCmd)
}
