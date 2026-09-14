package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/zeropsio/zbox-cli/internal/api"
)

var (
	flagShellSession string
	flagShellTicket  string
)

// A dropped connection can be resumed within this long with the session ticket, the terminal survives on the other side.
const shellReconnectWindow = 60 * time.Second

var shellCmd = &cobra.Command{
	Use:   "shell <id>",
	Short: "Open an interactive shell in a sandbox",
	Long:  "Opens a PTY through the API, so it works in every network mode. A parked sandbox is woken first. A dropped connection is resumed with the session ticket for a minute, --session and --ticket resume one by hand.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		fd := int(os.Stdin.Fd())
		cols, rows := 80, 24
		if term.IsTerminal(fd) {
			if w, h, err := term.GetSize(fd); err == nil {
				cols, rows = w, h
			}
			state, err := term.MakeRaw(fd)
			if err != nil {
				return err
			}
			defer term.Restore(fd, state)
		}

		// Stdin is read once for the whole command and handed to whichever connection is current, so a reconnect does not lose keystrokes typed meanwhile.
		input := make(chan []byte, 16)
		go func() {
			defer close(input)
			buf := make([]byte, 4096)
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
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)

		sessionId, ticket := flagShellSession, flagShellTicket
		deadline := time.Time{}
		for {
			conn, err := ws.Shell(ctx, args[0], cols, rows, sessionId, ticket)
			if err != nil {
				// A resume that cannot be dialled is retried while the ticket is good, a first dial fails for real.
				if deadline.IsZero() || time.Now().After(deadline) || ctx.Err() != nil {
					return err
				}
				time.Sleep(2 * time.Second)
				continue
			}
			// The first frame names the session and its ticket, the handle for a reconnect. A refused open or resume comes as a 4000 close with the API error as its reason.
			id, t, err := readSessionFrame(conn)
			if err != nil {
				conn.Close()
				if !deadline.IsZero() && sessionGone(err) {
					// The resume found the session gone: the shell exited during the drop.
					return nil
				}
				return err
			}
			if !deadline.IsZero() {
				fmt.Fprintf(os.Stderr, "\r\nresumed session %s\r\n", id)
			}
			sessionId, ticket = id, t
			closed, err := runShell(ctx, conn, fd, input, winch)
			conn.Close()
			if closed || ctx.Err() != nil || err == nil || ticket == "" {
				return err
			}
			// A drop, not an exit: resume the same session while the ticket is good.
			if deadline.IsZero() {
				deadline = time.Now().Add(shellReconnectWindow)
			} else if time.Now().After(deadline) {
				return fmt.Errorf("session %s could not be resumed: %w", sessionId, err)
			}
		}
	},
}

// sessionGone recognises the close the server sends when a resume names a session that has ended, which after a drop means the shell exited.
func sessionGone(err error) bool {
	return api.IsCode(closeReason(err), "sessionNotFound")
}

func readSessionFrame(conn *websocket.Conn) (id, ticket string, err error) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	t, data, err := conn.ReadMessage()
	if err != nil {
		return "", "", closeReason(err)
	}
	var frame struct {
		Session struct {
			Id     string `json:"id"`
			Ticket string `json:"ticket"`
		} `json:"session"`
	}
	if t != websocket.TextMessage || json.Unmarshal(data, &frame) != nil || frame.Session.Id == "" {
		return "", "", errors.New("the shell did not announce its session")
	}
	return frame.Session.Id, frame.Session.Ticket, nil
}

// closeReason turns the 4000 close the server answers a refused shell with into the same ApiError every other route gives, "<code>: <message>" being its reason text.
func closeReason(err error) error {
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != 4000 {
		return err
	}
	code, msg, found := strings.Cut(ce.Text, ": ")
	if !found {
		return api.ApiError{Status: 400, Message: ce.Text}
	}
	return api.ApiError{Status: 400, Code: code, Message: msg}
}

// runShell pumps one connection until it ends. closed says the shell exited (a normal close), otherwise the connection dropped and err says how.
func runShell(ctx context.Context, conn *websocket.Conn, fd int, input <-chan []byte, winch <-chan os.Signal) (closed bool, err error) {
	readErr := make(chan error, 1)
	go func() {
		for {
			t, data, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			if t == websocket.BinaryMessage {
				_, _ = os.Stdout.Write(data)
			}
		}
	}()
	for {
		select {
		case err := <-readErr:
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) || err == io.EOF || sessionGone(err) {
				return true, nil
			}
			return false, err
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			return true, nil
		case data, ok := <-input:
			if !ok {
				// Stdin ended (a pipe), the shell decides when it is done.
				input = nil
				continue
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				return false, err
			}
		case <-winch:
			if w, h, err := term.GetSize(fd); err == nil {
				msg, _ := json.Marshal(map[string]any{"resize": map[string]int{"cols": w, "rows": h}})
				_ = conn.WriteMessage(websocket.TextMessage, msg)
			}
		}
	}
}

func init() {
	shellCmd.Flags().StringVar(&flagShellSession, "session", "", "resume this PTY session instead of opening a new one")
	shellCmd.Flags().StringVar(&flagShellTicket, "ticket", "", "session ticket of the session to resume")
	rootCmd.AddCommand(shellCmd)
}
