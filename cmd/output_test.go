package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/zeropsio/zerops-go/apiError"

	"github.com/zeropsio/zbox-cli/internal/api"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

func TestPlainRowKeepsOneLinePerRow(t *testing.T) {
	got := plainRow([]string{"a\tb", "line one\nline two", "c"})
	if want := "a b\tline one line two\tc"; got != want {
		t.Fatalf("plainRow = %q, want %q", got, want)
	}
}

func TestRelative(t *testing.T) {
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Now().Add(-90 * time.Second), "1m ago"},
		{time.Now().Add(-3 * time.Hour), "3h ago"},
		{time.Now().Add(-49 * time.Hour), "2d ago"},
		{time.Now().Add(10*time.Minute + 5*time.Second), "in 10m"},
	} {
		if got := relative(c.at); got != c.want {
			t.Errorf("relative(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

func TestExitCodes(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{errors.New("boom"), exitFailed},
		{usagef("--port is required"), exitUsage},
		{fmt.Errorf("unknown flag: --nope"), exitUsage},
		{notFoundf("no base matches %q", "x"), exitNotFound},
		{api.ApiError{Status: 404, Code: "sandboxNotFound"}, exitNotFound},
		{api.ApiError{Status: 400, Code: "sandboxNotFound"}, exitNotFound},
		{api.ApiError{Status: 400, Code: "invalidUserInput"}, exitUsage},
		{api.ApiError{Status: 409, Code: "invalidSandboxStatus"}, exitConflict},
		{api.ApiError{Status: 401, Code: "unauthorized"}, exitUnauthorized},
		{api.ApiError{Status: 503}, exitUnavailable},
		{api.ApiError{Status: 504}, exitTimeout},
		{fmt.Errorf("wait: %w", api.ApiError{Status: 409}), exitConflict},
		{api.OperationFailed{Op: api.BaseOperation{Kind: "CREATE"}}, exitFailed},
		{waitTimeout{"x"}, exitTimeout},
		{context.Canceled, exitInterrupted},
		{notAuthorized{"not logged in"}, exitUnauthorized},
		{apiError.Error{HttpStatusCode: 403, ErrorCode: "insufficientPermissions"}, exitUnauthorized},
		{fmt.Errorf("stop: %w", apiError.Error{HttpStatusCode: 401, ErrorCode: "notAuthorized"}), exitUnauthorized},
		{apiError.Error{HttpStatusCode: 400, ErrorCode: "lastCpuModeChangeTooRecent"}, exitUsage},
		{apiError.Error{HttpStatusCode: 400, ErrorCode: "virtualMachineDiskScaleDownImpossible"}, exitUsage},
		{apiError.Error{HttpStatusCode: 400, ErrorCode: "serviceStackNotFound"}, exitNotFound},
		{apiError.Error{HttpStatusCode: 400, ErrorCode: "serviceStackNoActiveContainer"}, exitUnavailable},
		{apiError.Error{HttpStatusCode: 500, ErrorCode: "internalServerError"}, exitFailed},
		{zerops.Error{Status: 403, Code: "insufficientPermissions"}, exitUnauthorized},
		{zerops.Error{Status: 503, Message: "503 Service Unavailable"}, exitUnavailable},
	} {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestCoarse(t *testing.T) {
	for d, want := range map[time.Duration]string{42 * time.Second: "42s", 3*time.Minute + 59*time.Second: "3m", 2 * time.Hour: "2h", 49 * time.Hour: "2d"} {
		if got := coarse(d); got != want {
			t.Errorf("coarse(%s) = %q, want %q", d, got, want)
		}
	}
}

// Under --json an answer of the public API carries its own code and status, a bare error the code of its exit class.
func TestReportErrorJson(t *testing.T) {
	report := func(err error) map[string]any {
		t.Helper()
		r, w, perr := os.Pipe()
		if perr != nil {
			t.Fatal(perr)
		}
		stderr, json0 := os.Stderr, flagJson
		os.Stderr, flagJson = w, true
		reportError(err, exitCode(err))
		os.Stderr, flagJson = stderr, json0
		_ = w.Close()
		raw, _ := io.ReadAll(r)
		var out struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		return out.Error
	}
	got := report(fmt.Errorf("stop: %w", apiError.Error{HttpStatusCode: 403, ErrorCode: "insufficientPermissions", Message: "Insufficient permissions"}))
	if got["code"] != "insufficientPermissions" || got["status"] != 403.0 || got["exitCode"] != float64(exitUnauthorized) || got["message"] != "Insufficient permissions" {
		t.Errorf("public API error: %v", got)
	}
	if got = report(zerops.Error{Status: 502, Message: "502 Bad Gateway"}); got["code"] != "unavailable" || got["status"] != 502.0 {
		t.Errorf("public API error without a code: %v", got)
	}
	if got = report(errors.New("boom")); got["code"] != "failed" || got["exitCode"] != float64(exitFailed) {
		t.Errorf("bare error: %v", got)
	}
}
