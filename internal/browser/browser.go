// Package browser opens URLs in the system browser and receives the Zerops GUI hand-over (a personal access token, or a workspace key) on a loopback callback.
package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

// Open launches the system browser without waiting for it, so a missing browser is not fatal: the caller prints the URL anyway.
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

// The hand-over contract with the Zerops GUI (`/authorize-app`, the machinery Zerops Code uses): the CLI names a mode, a nonce and its loopback port, the GUI redirects to a path fixed on both sides with the credential in the fragment.
// The fragment never reaches a server, so the callback page runs a script that posts it back to the same loopback origin and clears it from history.
const (
	authorizePath = "/authorize-app"
	callbackPath  = "/zerops/authorized"
	deliverPath   = "/zerops/authorized/deliver"
	appName       = "zbox"
)

// Intent is what the GUI is asked to hand over.
type Intent string

const (
	IntentLogin     Intent = ""
	IntentRegister  Intent = "register"
	IntentWorkspace Intent = "workspace"
)

// open is what Handover launches the browser with, a variable so the test can play the browser.
var open = Open

// Handover opens the GUI for the intent and returns the fragment parameters of the callback. Errors the GUI reports (`access_denied`, `mint_failed`, `workspace_not_found`, `reveal_failed`, `workspace_not_ready`) come back as HandoverError.
func Handover(ctx context.Context, guiUrl string, intent Intent, params url.Values, timeout time.Duration, print func(string)) (url.Values, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	// The GUI redirects to localhost, which some resolvers answer with ::1 first; a second listener on the same port catches that. Best effort, a browser falls back to 127.0.0.1 anyway.
	ln6, err6 := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err6 == nil {
		defer ln6.Close()
	}

	state, err := nonce()
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	for k, vs := range params {
		query[k] = vs
	}
	query.Set("app", appName)
	query.Set("state", state)
	query.Set("port", strconv.Itoa(port))
	if intent != IntentLogin {
		query.Set("intent", string(intent))
	}
	target := guiUrl + authorizePath + "?" + query.Encode()

	results := make(chan url.Values, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, callbackPage)
	})
	mux.HandleFunc(deliverPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("state") != state {
			// A callback for another run or from somewhere else; the page shows the failure and the wait goes on.
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		select {
		case results <- r.PostForm:
		default:
		}
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(ln) }()
	if err6 == nil {
		go func() { _ = server.Serve(ln6) }()
	}
	defer server.Close()

	print("Opening " + target)
	if err := open(target); err != nil {
		print("Could not open a browser, open the URL above manually.")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case values := <-results:
		if code := values.Get("error"); code != "" {
			return nil, HandoverError{Code: code, Reason: values.Get("reason")}
		}
		return values, nil
	case <-ctx.Done():
		return nil, errors.New("timed out waiting for the browser")
	}
}

// HandoverError is a failure or a cancel reported by the GUI. Reason is optional free text from the GUI, a backend error code mostly.
type HandoverError struct{ Code, Reason string }

func (e HandoverError) Error() string {
	msg := e.message()
	if e.Reason != "" {
		msg += " (" + e.Reason + ")"
	}
	return msg
}

func (e HandoverError) message() string {
	switch e.Code {
	case "access_denied":
		return "cancelled in the browser"
	case "mint_failed":
		return "the Zerops GUI could not create the token"
	case "workspace_not_found":
		return "the Zerops GUI could not read that workspace with this account"
	case "reveal_failed":
		return "the Zerops GUI could not reveal the workspace API key"
	case "workspace_not_ready":
		return "the workspace has no hosts yet, it is still starting: try again in a moment"
	}
	return "the Zerops GUI answered " + e.Code
}

func nonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// The page posts the fragment back and scrubs it from the address bar and history before saying anything.
const callbackPage = `<!doctype html><html><head><meta charset="utf-8"><title>zbox</title></head>
<body style="font-family:sans-serif;padding:2rem"><h2 id="h">Finishing up…</h2><p id="p"></p>
<script>
(async () => {
  const fragment = location.hash.slice(1);
  history.replaceState(null, '', location.pathname);
  const h = document.getElementById('h'), p = document.getElementById('p');
  if (!fragment) { h.textContent = 'Nothing arrived.'; p.textContent = 'Run the zbox command again.'; return; }
  try {
    const res = await fetch('` + deliverPath + `', { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body: fragment });
    if (!res.ok) throw new Error(await res.text());
    h.textContent = 'zbox CLI is set up.'; p.textContent = 'You can close this window.';
  } catch (e) {
    h.textContent = 'Could not hand over to the CLI.'; p.textContent = String(e.message || e) + '. Run the zbox command again.';
  }
})();
</script></body></html>`
