package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/zeropsio/zbox-cli/internal/api"
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
	Long:  "By default the Zerops GUI opens on a consent page, the user signs in there, confirms the code the CLI printed, and the CLI receives a session, which it refreshes itself. `--token` stores a personal access token from the Zerops GUI instead. `--email` (with `--password`, asked for without echo when missing) opens the session from the terminal; that path cannot answer two-factor authentication.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		switch {
		case flagLoginToken != "":
			c.Token = flagLoginToken
			c.Session = nil
		case flagLoginEmail != "" || flagLoginPassword != "":
			email, password, err := askCredentials(flagLoginEmail, flagLoginPassword)
			if err != nil {
				return err
			}
			session, err := zerops.Login(cmd.Context(), c.ApiUrl, email, password)
			if err != nil {
				return err
			}
			storeSession(&c, session)
		default:
			session, err := zerops.DeviceLogin(cmd.Context(), c.ApiUrl, c.EffectiveGuiUrl(), printLine)
			if err != nil {
				return err
			}
			storeSession(&c, session)
		}
		return finishLogin(cmd, c)
	},
}

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Create a Zerops account and log the CLI in",
	Long:  "By default the Zerops GUI opens on the consent page of a login, where a newcomer registers first, and the CLI receives the session of the new account. With `--email` the account is created from the terminal (password, name and organisation asked for when missing).",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		if flagLoginEmail == "" {
			session, err := zerops.DeviceLogin(cmd.Context(), c.ApiUrl, c.EffectiveGuiUrl(), printLine)
			if err != nil {
				return err
			}
			storeSession(&c, session)
			return finishLogin(cmd, c)
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
		storeSession(&c, session)
		return finishLogin(cmd, c)
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "End the Zerops login and forget it",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		// The zproxy accesses minted with this login are burnt too, so a token the config kept cannot outlive the login; one that is already dead or unreachable is simply dropped.
		burnt := 0
		for i, w := range c.Workspaces {
			if w.Access == nil {
				continue
			}
			if err := api.New(w.Access.Url, w.Access.Token).Logout(cmd.Context()); err == nil {
				burnt++
			}
			c.Workspaces[i].Access = nil
		}
		// The session is ended on the API as well, so the refresh token the file held is dead whatever happens to a copy of the file.
		if c.Session != nil {
			_ = zerops.New(c.ApiUrl, c.Session.AccessToken).Logout(cmd.Context())
		}
		c.Access = nil
		c.Session = nil
		c.Token = ""
		c.ClientId = ""
		if err := c.Save(); err != nil {
			return err
		}
		if burnt > 0 {
			say("Logged out, %d workspace access(es) revoked.", burnt)
		} else {
			say("Logged out.")
		}
		return nil
	},
}

func storeSession(c *config.Config, s zerops.Session) {
	c.Session = &config.Session{AccessToken: s.AccessToken, ExpiresAt: s.ExpiresAt, RefreshToken: s.RefreshToken}
	c.Token = ""
}

// finishLogin checks the login just obtained against the API (the stored one, never ZBOX_TOKEN from the environment), so a typo in a token is caught here and not by the next command, and remembers the organisation the workspace will be created in. The hidden URL flags are stored with the login, which is how a config moves environments.
func finishLogin(cmd *cobra.Command, c config.Config) error {
	token := c.Token
	if c.Session != nil {
		token = c.Session.AccessToken
	}
	user, err := zerops.New(c.ApiUrl, token).UserInfo(cmd.Context())
	if err != nil {
		return fmt.Errorf("login check failed: %w", err)
	}
	c.ClientId = user.ClientId
	c.AdoptOverrides()
	if err := c.Save(); err != nil {
		return err
	}
	say("Logged in as %s (%s).", user.Email, user.Account)
	return nil
}

func printLine(s string) {
	fmt.Fprintln(os.Stderr, s)
}

// ensureSudo puts the session into sudo mode through the consent page when it is not in it yet. A personal access token cannot be put there by the API, its own sudo state stands.
func ensureSudo(cmd *cobra.Command, c config.Config, zc *zerops.Client) error {
	inSudo, err := zc.InSudo(cmd.Context())
	if err != nil || inSudo {
		return err
	}
	if c.Session == nil || os.Getenv(config.EnvToken) != "" {
		return notAuthorized{"this needs the login in sudo mode, which a personal access token cannot enter: run `zbox login` for a browser login"}
	}
	return zc.Sudo(cmd.Context(), c.EffectiveGuiUrl(), printLine)
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
	loginCmd.Flags().StringVar(&flagLoginToken, "token", "", "personal access token from the Zerops GUI, stored as is")
	registerCmd.Flags().StringVar(&flagRegisterName, "name", "", "your full name")
	registerCmd.Flags().StringVar(&flagRegisterOrg, "organisation", "", "name of the organisation the account creates")
	rootCmd.AddCommand(loginCmd, registerCmd, logoutCmd)
}
