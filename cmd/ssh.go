package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"

	"github.com/zeropsio/zbox-cli/internal/api"
	"github.com/zeropsio/zbox-cli/internal/config"
)

// SSH to a sandbox goes through the API: the local ssh runs with a ProxyCommand (`zbox ssh-proxy`) that carries the connection over a
// WebSocket to the agent's own SSH server, so it works in every network mode without the project VPN and without a key, the API
// authenticated the caller already. The host key is pinned from `GET /sandbox/{id}/ssh` into the CLI's own known_hosts under the
// alias zbox-<id>, which is also the host name the ssh-config block matches.

const sshHostPrefix = "zbox-"

var sshCmd = &cobra.Command{
	Use:   "ssh <id|name> [-- ssh arguments]",
	Short: "Open SSH to a sandbox through the API",
	Long:  "Runs the local ssh with a ProxyCommand that carries the connection over the API, so it works in every network mode without the project VPN and without a key. The host key is pinned from the API. Arguments after -- go to ssh, a trailing command runs instead of the shell. `zbox ssh-config --install` makes plain ssh, scp, rsync and editors reach a sandbox as zbox-<id>.",
	Example: `  zbox ssh app
  zbox ssh app -- uname -a
  zbox ssh app -- -L 3000:localhost:3000`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		id, err := resolveSandbox(cmd.Context(), ws, args[0])
		if err != nil {
			return err
		}
		knownHosts, err := pinHostKey(cmd.Context(), ws, id)
		if err != nil {
			return err
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		sshArgs := append(sshOptions(exe, knownHosts), sshHostPrefix+id)
		sshArgs = append(sshArgs, args[1:]...)
		c := exec.CommandContext(cmd.Context(), "ssh", sshArgs...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				os.Exit(exit.ExitCode())
			}
			return err
		}
		return nil
	},
}

// sshOptions is what the ssh-config block says, given on the command line so `zbox ssh` works without the block installed.
func sshOptions(exe, knownHosts string) []string {
	return []string{
		"-o", "ProxyCommand=" + proxyCommand(exe, flagWorkspace),
		"-o", "User=root",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "ServerAliveInterval=20",
	}
}

// proxyCommand is the ProxyCommand line. A workspace override is carried only for the one `zbox ssh` run that named it; the installed block always follows the current workspace.
func proxyCommand(exe, workspace string) string {
	command := fmt.Sprintf("%q ssh-proxy %%h", exe)
	if workspace != "" {
		command += fmt.Sprintf(" --workspace %q", workspace)
	}
	return command
}

var sshProxyCmd = &cobra.Command{
	Use:    "ssh-proxy <zbox-id|id>",
	Short:  "ProxyCommand of zbox ssh: relays stdin and stdout to a sandbox's SSH server over the API",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		id := strings.TrimPrefix(args[0], sshHostPrefix)
		// The pin happens here too, so `ssh zbox-<id>` through the config block verifies a sandbox it never saw.
		if _, err := pinHostKey(cmd.Context(), ws, id); err != nil {
			return err
		}
		conn, err := ws.SshConnect(cmd.Context(), id)
		if err != nil {
			return err
		}
		defer conn.Close()
		return relayStdio(cmd.Context(), conn)
	},
}

// relayStdio pumps stdin into binary frames and binary frames onto stdout until either side ends. A 4000 close is the API's refusal.
func relayStdio(ctx context.Context, conn *websocket.Conn) error {
	readErr := make(chan error, 1)
	go func() {
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			if kind == websocket.BinaryMessage {
				if _, err := os.Stdout.Write(data); err != nil {
					readErr <- err
					return
				}
			}
		}
	}()
	input := make(chan []byte, 16)
	go func() {
		defer close(input)
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				input <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case err := <-readErr:
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) || err == io.EOF {
				return nil
			}
			return closeReason(err)
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			return nil
		case data, ok := <-input:
			if !ok {
				// ssh closed our stdin, the connection is over.
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				return nil
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				return err
			}
		}
	}
}

var flagSshConfigInstall bool

var sshConfigCmd = &cobra.Command{
	Use:   "ssh-config",
	Short: "Print the ssh config block that makes zbox-<id> a host for ssh, scp, rsync and editors",
	Long:  "Prints a Host zbox-* block whose ProxyCommand carries the connection over the API. --install writes it next to the zbox config and adds an Include line to ~/.ssh/config, so `ssh zbox-<id>`, `scp`, `rsync`, `sshfs` and the remote-SSH extensions of VS Code and JetBrains reach any sandbox of the current workspace.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// The block is persistent and follows the current workspace, so a one-run override has no place in an install.
		if flagSshConfigInstall {
			if err := refuseOverride("zbox ssh-config --install"); err != nil {
				return err
			}
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		knownHosts, err := sshFile("known_hosts")
		if err != nil {
			return err
		}
		block := sshConfigBlock(exe, knownHosts)
		if !flagSshConfigInstall {
			fmt.Print(block)
			return nil
		}
		path, err := sshFile("ssh_config")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
			return err
		}
		included, err := ensureInclude(path)
		if err != nil {
			return err
		}
		if included {
			result(path, "Wrote %s and included it from ~/.ssh/config. Connect with ssh zbox-<id>.", path)
		} else {
			result(path, "Wrote %s, ~/.ssh/config already includes it. Connect with ssh zbox-<id>.", path)
		}
		return nil
	},
}

func sshConfigBlock(exe, knownHosts string) string {
	return fmt.Sprintf(`# zbox: SSH to a sandbox of the current workspace through the API, as zbox-<sandbox id>. Written by zbox ssh-config --install.
Host %s*
  ProxyCommand %s
  User root
  StrictHostKeyChecking yes
  UserKnownHostsFile %s
  ServerAliveInterval 20
`, sshHostPrefix, proxyCommand(exe, ""), knownHosts)
}

// ensureInclude puts the Include first in ~/.ssh/config, where it applies to every host, and reports whether it had to.
func ensureInclude(path string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	configPath := filepath.Join(dir, "config")
	existing, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	line := "Include " + path
	for _, l := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(l) == line {
			return false, nil
		}
	}
	content := line + "\n"
	if len(existing) > 0 {
		content += "\n" + string(existing)
	}
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// sshFile is a file next to the zbox config, where the CLI keeps what it writes for ssh.
func sshFile(name string) (string, error) {
	configPath, err := config.Path()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// pinHostKey writes the sandbox's host key under its alias into the CLI's known_hosts, replacing an earlier line for the same alias,
// and answers the file's path.
func pinHostKey(ctx context.Context, ws *api.Workspace, id string) (string, error) {
	info, err := ws.Ssh(ctx, id)
	if err != nil {
		return "", err
	}
	if info.HostKey == nil {
		return "", fmt.Errorf("sandbox %s has no SSH host key yet: it is from before the SSH server, park and wake it (or fork it) to give it one", id)
	}
	path, err := sshFile("known_hosts")
	if err != nil {
		return "", err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	alias := sshHostPrefix + id
	line := alias + " " + *info.HostKey
	lines := []string{}
	for _, l := range strings.Split(string(existing), "\n") {
		if l == line {
			// Already pinned as it is: nothing to write, so parallel sessions of one sandbox never touch the file ssh is reading.
			return path, nil
		}
		if l == "" || strings.HasPrefix(l, alias+" ") {
			continue
		}
		lines = append(lines, l)
	}
	lines = append(lines, line)
	return path, writeAtomic(path, []byte(strings.Join(lines, "\n")+"\n"))
}

// writeAtomic replaces the file through a temporary one in the same directory and a rename, so an ssh that reads the file while another proxy pins never sees it truncated or half written, and two pins at once leave one whole version rather than a torn one.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func init() {
	sshConfigCmd.Flags().BoolVar(&flagSshConfigInstall, "install", false, "write the block next to the zbox config and include it from ~/.ssh/config")
}
