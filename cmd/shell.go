package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var shellCmd = &cobra.Command{
	Use:   "shell <id>",
	Short: "Open an interactive shell in a sandbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadConfig()
		if err != nil {
			return err
		}
		client, err := sandboxClient(c)
		if err != nil {
			return err
		}
		fd := int(os.Stdin.Fd())
		cols, rows := 80, 24
		if term.IsTerminal(fd) {
			if w, h, err := term.GetSize(fd); err == nil {
				cols, rows = w, h
			}
		}
		conn, resp, err := websocket.DefaultDialer.DialContext(cmd.Context(), client.ShellUrl(args[0], cols, rows), nil)
		if err != nil {
			if resp != nil {
				var envelope struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if json.NewDecoder(resp.Body).Decode(&envelope) == nil && envelope.Error.Message != "" {
					return fmt.Errorf("%s", envelope.Error.Message)
				}
			}
			return err
		}
		defer conn.Close()

		if term.IsTerminal(fd) {
			state, err := term.MakeRaw(fd)
			if err != nil {
				return err
			}
			defer term.Restore(fd, state)
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				t, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if t == websocket.BinaryMessage {
					_, _ = os.Stdout.Write(data)
				}
			}
		}()

		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := os.Stdin.Read(buf)
				if n > 0 {
					if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
						return
					}
				}
				if err != nil {
					if err != io.EOF {
						return
					}
					return
				}
			}
		}()

		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)

		for {
			select {
			case <-done:
				return nil
			case <-cmd.Context().Done():
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				return nil
			case <-winch:
				if w, h, err := term.GetSize(fd); err == nil {
					msg, _ := json.Marshal(map[string]any{"resize": map[string]int{"cols": w, "rows": h}})
					_ = conn.WriteMessage(websocket.TextMessage, msg)
				}
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(shellCmd)
}
