// Package browser opens the GUI for login or registration and receives the resulting token on a loopback callback.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

const callbackPath = "/callback"

// Open launches the system browser without waiting for it, so a missing browser is not fatal: the URL is printed anyway.
func Open(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

// ReceiveToken starts a loopback HTTP listener and returns the token the GUI redirects back with.
// The GUI is opened at guiUrl + "#/" + page with the callback URL as a query parameter.
func ReceiveToken(ctx context.Context, guiUrl, page string, timeout time.Duration, print func(string)) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()

	callback := fmt.Sprintf("http://%s%s", ln.Addr().String(), callbackPath)
	target := fmt.Sprintf("%s/#/%s?callback=%s", guiUrl, page, url.QueryEscape(callback))

	tokens := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>zbox</title><body style=\"font-family:sans-serif;padding:2rem\"><h2>zbox CLI is logged in.</h2><p>You can close this window.</p></body>"))
		select {
		case tokens <- token:
		default:
		}
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(ln) }()
	defer server.Close()

	print("Opening " + target)
	if err := Open(target); err != nil {
		print("Could not open a browser, open the URL above manually.")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case t := <-tokens:
		return t, nil
	case <-ctx.Done():
		return "", errors.New("timed out waiting for the browser login")
	}
}
