package cmd

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/zeropsio/zbox-cli/internal/api"
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
