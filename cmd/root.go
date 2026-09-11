// Package cmd holds the cobra commands of the zbox CLI.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/config"
	"github.com/zeropsio/zbox-cli/internal/zboxapi"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

var (
	flagApiUrl    string
	flagGuiUrl    string
	flagBinaryUrl string
)

var rootCmd = &cobra.Command{
	Use:           "zbox",
	Short:         "Agent sandboxes on Zerops",
	Long:          "zbox creates a sandbox pool in your Zerops account and manages sandboxes inside it.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		var exit exitError
		if errors.As(err, &exit) {
			os.Exit(exit.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return err
	}
	return nil
}

// exitError carries a remote exit code (zbox exec) to the process exit status without printing anything.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&flagApiUrl, "public-api-url", "", "Zerops public API base URL")
	pf.StringVar(&flagGuiUrl, "gui-url", "", "zbox web app URL used for browser login, overrides guiUrl from the config")
	pf.StringVar(&flagBinaryUrl, "binary-url", config.DefaultBinaryUrl, "download URL of the orchestrator binary used by setup")
	_ = pf.MarkHidden("public-api-url")
	_ = pf.MarkHidden("gui-url")
	_ = pf.MarkHidden("binary-url")
}

func loadConfig() (config.Config, error) {
	c, err := config.Load()
	if err != nil {
		return c, err
	}
	if flagApiUrl != "" {
		c.ApiUrl = flagApiUrl
	}
	return c, nil
}

func zeropsClient(c config.Config) (*zerops.Client, error) {
	token := c.EffectiveToken()
	if token == "" {
		return nil, errors.New("not logged in, run `zbox login` first")
	}
	return zerops.New(c.ApiUrl, token), nil
}

func sandboxClient(c config.Config) (*zboxapi.Client, error) {
	if !c.HasSandboxPool() {
		return nil, errors.New("no sandbox pool yet, run `zbox setup` first")
	}
	return zboxapi.New(c.ZboxUrl), nil
}
