package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/browser"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

var flagLoginToken string

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in with a Zerops personal access token, or through the browser",
	RunE: func(cmd *cobra.Command, args []string) error {
		return browserAuth(cmd, "cli-login", flagLoginToken)
	},
}

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Create a Zerops account in the browser and log the CLI in",
	RunE: func(cmd *cobra.Command, args []string) error {
		return browserAuth(cmd, "cli-register", "")
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Forget the stored token",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		c.Token = ""
		c.ClientId = ""
		if err := c.Save(); err != nil {
			return err
		}
		fmt.Println("Logged out.")
		return nil
	},
}

func browserAuth(cmd *cobra.Command, page, token string) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if token == "" {
		guiUrl := c.EffectiveGuiUrl()
		if flagGuiUrl != "" {
			guiUrl = flagGuiUrl
		}
		token, err = browser.ReceiveToken(cmd.Context(), guiUrl, page, 5*time.Minute, func(s string) { fmt.Println(s) })
		if err != nil {
			return err
		}
	}
	user, err := zerops.New(c.ApiUrl, token).UserInfo(cmd.Context())
	if err != nil {
		return fmt.Errorf("token check failed: %w", err)
	}
	c.Token = token
	c.ClientId = user.ClientId
	if err := c.Save(); err != nil {
		return err
	}
	fmt.Printf("Logged in as %s (%s).\n", user.Email, user.Account)
	return nil
}

func init() {
	loginCmd.Flags().StringVar(&flagLoginToken, "token", "", "personal access token, skips the browser flow")
	rootCmd.AddCommand(loginCmd, registerCmd, logoutCmd)
}
