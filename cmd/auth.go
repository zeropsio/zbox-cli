package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/zeropsio/zbox-cli/internal/browser"
	"github.com/zeropsio/zbox-cli/internal/config"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

var (
	flagLoginToken    string
	flagLoginEmail    string
	flagLoginPassword string
	flagRegisterName  string
	flagRegisterOrg   string
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in to Zerops through the browser, or with a token or email and password",
	Long:  "By default the Zerops GUI opens, the user confirms there and a personal access token is handed back to the CLI on this machine and stored. `--token` stores a given token instead. `--email` (with `--password`, asked for without echo when missing) logs in from the terminal and mints the token itself; that path cannot answer two-factor authentication.",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		token := flagLoginToken
		switch {
		case token != "":
		case flagLoginEmail != "" || flagLoginPassword != "":
			email, password, err := askCredentials(flagLoginEmail, flagLoginPassword)
			if err != nil {
				return err
			}
			session, err := zerops.Login(cmd.Context(), c.ApiUrl, email, password)
			if err != nil {
				return err
			}
			token, err = session.MintToken(cmd.Context(), tokenName())
			if err != nil {
				return err
			}
		default:
			token, err = handoverToken(cmd, c, browser.IntentLogin)
			if err != nil {
				return err
			}
		}
		return storeLogin(cmd, c, token)
	},
}

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Create a Zerops account and log the CLI in",
	Long:  "By default the Zerops GUI opens at the registration and hands a personal access token back to the CLI. With `--email` the account is created from the terminal (password, name and organisation asked for when missing).",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		if flagLoginEmail == "" {
			token, err := handoverToken(cmd, c, browser.IntentRegister)
			if err != nil {
				return err
			}
			return storeLogin(cmd, c, token)
		}
		email, password, err := askCredentials(flagLoginEmail, flagLoginPassword)
		if err != nil {
			return err
		}
		name, err := askLine("Your name", flagRegisterName)
		if err != nil {
			return err
		}
		org, err := askLine("Organisation name", flagRegisterOrg)
		if err != nil {
			return err
		}
		session, err := zerops.Register(cmd.Context(), c.ApiUrl, zerops.Registration{Email: email, Password: password, Name: name, AccountName: org})
		if err != nil {
			return err
		}
		token, err := session.MintToken(cmd.Context(), tokenName())
		if err != nil {
			return err
		}
		return storeLogin(cmd, c, token)
	},
}

// handoverToken runs the browser hand-over and returns the personal access token the GUI minted.
func handoverToken(cmd *cobra.Command, c config.Config, intent browser.Intent) (string, error) {
	values, err := browser.Handover(cmd.Context(), c.EffectiveGuiUrl(), intent, nil, 5*time.Minute, func(s string) { fmt.Fprintln(os.Stderr, s) })
	if err != nil {
		return "", err
	}
	token := values.Get("token")
	if token == "" {
		return "", errors.New("the Zerops GUI answered without a token")
	}
	return token, nil
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
		say("Logged out.")
		return nil
	},
}

// storeLogin checks the token against the API, so a typo is caught here and not by the next command, and remembers the organisation the workspace will be created in.
func storeLogin(cmd *cobra.Command, c config.Config, token string) error {
	user, err := zerops.New(c.ApiUrl, token).UserInfo(cmd.Context())
	if err != nil {
		return fmt.Errorf("token check failed: %w", err)
	}
	c.Token = token
	c.ClientId = user.ClientId
	if err := c.Save(); err != nil {
		return err
	}
	say("Logged in as %s (%s).", user.Email, user.Account)
	return nil
}

// tokenName names the personal access token after the machine, so the user can tell it apart in the Zerops GUI and revoke it.
func tokenName() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "cli"
	}
	return fmt.Sprintf("zbox CLI %s %s", host, time.Now().Format("2006-01-02"))
}

func askCredentials(email, password string) (string, string, error) {
	email, err := askLine("Email", email)
	if err != nil {
		return "", "", err
	}
	if password == "" {
		password, err = askSecret("Password")
		if err != nil {
			return "", "", err
		}
	}
	return email, password, nil
}

// askLine returns the given value when there is one, otherwise it prompts. Without a terminal there is nothing to prompt on and the flag is the only way.
func askLine(label, given string) (string, error) {
	if given != "" {
		return given, nil
	}
	if !stdinTerminal {
		return "", usagef("%s is needed and stdin is not a terminal, pass it as a flag", strings.ToLower(label))
	}
	fmt.Fprintf(os.Stderr, "%s: ", label)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New(strings.ToLower(label) + " must not be empty")
	}
	return line, nil
}

// askSecret reads without echo; the password never shows on the terminal and never lands in the shell history unless the user chose --password.
func askSecret(label string) (string, error) {
	if !stdinTerminal {
		return "", usagef("%s is needed and stdin is not a terminal, pass it as a flag or use --token", strings.ToLower(label))
	}
	fmt.Fprintf(os.Stderr, "%s: ", label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New(strings.ToLower(label) + " must not be empty")
	}
	return string(b), nil
}

func init() {
	for _, c := range []*cobra.Command{loginCmd, registerCmd} {
		c.Flags().StringVar(&flagLoginEmail, "email", "", "account email, switches from the browser to the terminal login")
		c.Flags().StringVar(&flagLoginPassword, "password", "", "account password, asked for without echo when missing")
	}
	loginCmd.Flags().StringVar(&flagLoginToken, "token", "", "personal access token, stored as is")
	registerCmd.Flags().StringVar(&flagRegisterName, "name", "", "your full name")
	registerCmd.Flags().StringVar(&flagRegisterOrg, "organisation", "", "name of the organisation the account creates")
	rootCmd.AddCommand(loginCmd, registerCmd, logoutCmd)
}
