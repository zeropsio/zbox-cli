package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The wrapper sends the bearer, decodes the 200 body into the Base type and turns both error shapes into an ApiError.
func TestWrapper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			w.Write([]byte(`{"code":"unauthorized","message":"Unauthorized"}`))
			return
		}
		switch r.URL.Path {
		case "/api/rest/sandbox/abcdefgh":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"abcdefgh","status":"RUNNING","hosts":[{"port":3000,"access":"MEMBERS","url":"https://3000-abcdefgh-ws.example"}],"created":"2026-09-14T10:00:00Z"}`))
		case "/api/rest/sandbox-list":
			w.Write([]byte(`{"items":[{"id":"abcdefgh"},{"id":"ijklmnop"}]}`))
		case "/api/rest/sandbox/missing":
			w.WriteHeader(404)
			w.Write([]byte(`{"error":{"code":"sandboxNotFound","message":"Sandbox not found","meta":[{"metadata":{"id":["must exist"]}}]}}`))
		case "/api/rest/sandbox":
			if r.Header.Get("Idempotency-Key") == "" {
				w.WriteHeader(400)
				w.Write([]byte(`{"code":"invalidUserInput","message":"no key"}`))
				return
			}
			w.Write([]byte(`{"operation":{"id":"op1","kind":"CREATE","status":"PENDING"},"sandboxes":[{"id":"abcdefgh","status":"CREATING"}]}`))
		default:
			w.WriteHeader(500)
			w.Write([]byte("boom"))
		}
	}))
	defer srv.Close()

	ws := New(srv.URL, "k")
	ctx := context.Background()
	s, err := ws.Sandbox(ctx, "abcdefgh")
	if err != nil || s.Status != BaseSandboxStatusRUNNING || len(s.Hosts) != 1 || s.Hosts[0].Url != "https://3000-abcdefgh-ws.example" {
		t.Fatalf("get: %v %+v", err, s)
	}
	if Time(s.Created).IsZero() {
		t.Fatalf("created not parsed: %q", s.Created)
	}
	list, err := ws.Sandboxes(ctx, GetSandboxListParams{})
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	_, err = ws.Sandbox(ctx, "missing")
	if !IsCode(err, "sandboxNotFound") || err.Error() != "Sandbox not found (sandboxNotFound): id must exist" || MetaValue(err, "id") != "must exist" || MetaValue(err, "other") != "" {
		t.Fatalf("envelope error: %v", err)
	}
	spawn, err := ws.CreateSandbox(ctx, RequestPostSandbox{NetworkMode: RequestPostSandboxNetworkModeINTERNET})
	if err != nil || spawn.Operation.Id != "op1" || len(spawn.Sandboxes) != 1 {
		t.Fatalf("create: %v %+v", err, spawn)
	}
	_, err = ws.Operation(ctx, "x")
	if e, ok := err.(ApiError); !ok || e.Status != 500 || e.Message != "boom" {
		t.Fatalf("plain error: %v", err)
	}
	_, err = New(srv.URL, "wrong").Sandbox(ctx, "abcdefgh")
	if !IsCode(err, "unauthorized") {
		t.Fatalf("flat error: %v", err)
	}
}

func TestProxiedFromUrl(t *testing.T) {
	if !New("https://proxy.example/service-api", "t").Proxied() {
		t.Error("a zproxy client built without Renew is not told apart")
	}
	if New("https://zbox-8080.example", "k").Proxied() {
		t.Error("a direct client counts as proxied")
	}
}
