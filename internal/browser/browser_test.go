package browser

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// playBrowser stands in for the GUI and the browser: it reads the request the CLI opened, answers the way the GUI would and posts the fragment back like the callback page does.
func playBrowser(t *testing.T, answer func(query url.Values) url.Values) func(string) error {
	return func(target string) error {
		u, err := url.Parse(target)
		if err != nil {
			return err
		}
		if u.Path != authorizePath || u.Query().Get("app") != appName || u.Query().Get("state") == "" || u.Query().Get("port") == "" {
			t.Errorf("unexpected authorize URL %s", target)
		}
		go func() {
			callback := "http://127.0.0.1:" + u.Query().Get("port") + callbackPath
			res, err := http.Get(callback)
			if err != nil {
				t.Error(err)
				return
			}
			page, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if !strings.Contains(string(page), deliverPath) {
				t.Errorf("callback page does not post to %s", deliverPath)
			}
			fragment := answer(u.Query())
			res, err = http.Post("http://127.0.0.1:"+u.Query().Get("port")+deliverPath, "application/x-www-form-urlencoded", strings.NewReader(fragment.Encode()))
			if err != nil {
				t.Error(err)
				return
			}
			res.Body.Close()
		}()
		return nil
	}
}

func TestHandoverToken(t *testing.T) {
	open = playBrowser(t, func(q url.Values) url.Values {
		if q.Get("intent") != "register" {
			t.Errorf("intent %q, want register", q.Get("intent"))
		}
		return url.Values{"token": {"pat-1"}, "state": {q.Get("state")}}
	})
	values, err := Handover(context.Background(), "https://gui.test", IntentRegister, nil, 5*time.Second, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("token") != "pat-1" {
		t.Errorf("token %q", values.Get("token"))
	}
}

func TestHandoverWorkspace(t *testing.T) {
	open = playBrowser(t, func(q url.Values) url.Values {
		if q.Get("intent") != "workspace" || q.Get("serviceStackId") != "svc" {
			t.Errorf("query %v", q)
		}
		return url.Values{"apiHost": {"https://h"}, "apiKey": {"k"}, "serviceStackId": {"svc"}, "state": {q.Get("state")}}
	})
	values, err := Handover(context.Background(), "https://gui.test", IntentWorkspace, url.Values{"serviceStackId": {"svc"}}, 5*time.Second, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("apiKey") != "k" || values.Get("apiHost") != "https://h" {
		t.Errorf("values %v", values)
	}
}

func TestHandoverErrorAndState(t *testing.T) {
	open = playBrowser(t, func(q url.Values) url.Values {
		return url.Values{"error": {"access_denied"}, "state": {q.Get("state")}}
	})
	_, err := Handover(context.Background(), "https://gui.test", IntentLogin, nil, 5*time.Second, func(string) {})
	var he HandoverError
	if !errors.As(err, &he) || he.Code != "access_denied" {
		t.Fatalf("want access_denied, got %v", err)
	}

	// A wrong state is refused and the wait goes on until the timeout.
	open = playBrowser(t, func(q url.Values) url.Values {
		return url.Values{"token": {"stolen"}, "state": {"other"}}
	})
	_, err = Handover(context.Background(), "https://gui.test", IntentLogin, nil, 300*time.Millisecond, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout, got %v", err)
	}
}
