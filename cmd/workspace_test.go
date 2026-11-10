package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/zeropsio/zbox-cli/internal/config"
	"github.com/zeropsio/zbox-cli/internal/waiting"
	"github.com/zeropsio/zbox-cli/internal/zerops"
)

// Output names the host of the Zerops Box the config names, the default one only when none is stored.
func TestBoxHost(t *testing.T) {
	if got := boxHost(config.Config{}); got != "box.zerops.io" {
		t.Error(got)
	}
	host := boxHost(config.Config{BoxUrl: "https://box.dev.example/"})
	if host != "box.dev.example" {
		t.Error(host)
	}
	joined := strings.Join(waiting.Notes(host), "\n")
	if strings.Contains(joined, "{box}") || strings.Contains(joined, "box.zerops.io") || !strings.Contains(joined, "Zerops Box (box.dev.example)") {
		t.Errorf("notes:\n%s", joined)
	}
}

// The console is the workspace's page in Zerops Box, under the origin the config names.
func TestConsoleUrl(t *testing.T) {
	if got := consoleUrl("https://box.zerops.io", "abc123"); got != "https://box.zerops.io/workspace/abc123" {
		t.Error(got)
	}
}

// The ORGANIZATION column comes with an account of several organisations, between the hostname and the project, on the rows not in the account too.
func TestPrintAccountWorkspaces(t *testing.T) {
	c := config.Config{Workspaces: []config.Workspace{{Name: "mine", ServiceId: "s1"}, {Name: "byhand", WorkspaceUrl: "https://byhand.example", ApiKey: "k"}}}
	stacks := []zerops.WorkspaceStack{{ServiceId: "s1", Hostname: "zbox", ProjectName: "p", OrganizationName: "Team", Status: "ACTIVE"}}
	capture := func(withOrg bool) []string {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout := os.Stdout
		os.Stdout = w
		printAccountWorkspaces(c, stacks, withOrg)
		os.Stdout = stdout
		_ = w.Close()
		out, _ := io.ReadAll(r)
		return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	}
	lines := capture(true)
	want := []string{
		"\tSERVICE ID\tNAME\tHOSTNAME\tORGANIZATION\tPROJECT\tSTATUS\tSTORED",
		" \ts1\tmine\tzbox\tTeam\tp\tACTIVE\tyes",
		" \t-\tbyhand\tbyhand.example\t-\t-\tnot in this account\tyes",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("with organisations:\n%s", strings.Join(lines, "\n"))
	}
	if lines = capture(false); strings.Contains(lines[0], "ORGANIZATION") || strings.Count(lines[1], "\t") != 6 {
		t.Errorf("one organisation:\n%s", strings.Join(lines, "\n"))
	}
}

// A resize names at least one thing to change, nothing under the floor, and a cpu mode the platform knows; the mode is spelled as the platform does.
func TestResizeRequest(t *testing.T) {
	for _, tc := range []struct {
		cpu, ram, disk int
		mode           string
		want           zerops.Resize
		usage          bool
	}{
		{usage: true},
		{cpu: 1, usage: true},
		{ram: 1, usage: true},
		{disk: -10, usage: true},
		{disk: 9, want: zerops.Resize{Disk: 9}},
		{mode: "turbo", usage: true},
		{ram: 8, want: zerops.Resize{Ram: 8}},
		{cpu: 4, ram: 16, disk: 50, want: zerops.Resize{Cpu: 4, Ram: 16, Disk: 50}},
		{mode: "Dedicated", want: zerops.Resize{CpuMode: zerops.CpuDedicated}},
		{cpu: 2, mode: "shared", want: zerops.Resize{Cpu: 2, CpuMode: zerops.CpuShared}},
	} {
		got, err := resizeRequest(tc.cpu, tc.ram, tc.disk, tc.mode)
		if tc.usage {
			if err == nil || exitCode(err) != exitUsage {
				t.Errorf("%+v: want a usage error, got %v", tc, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%+v: got %+v %v", tc, got, err)
		}
	}
	if _, err := resizeRequest(1, 8, 0, ""); err == nil || err.Error() != "workspace size: 1 cpu is under the floor of 2 cpu" {
		t.Errorf("the floor message names only what was given: %v", err)
	}
}

// The disk only grows: a smaller --disk fails before the API is called, the same or a bigger one passes, and a resize without --disk passes.
func TestCheckResize(t *testing.T) {
	for r, ok := range map[zerops.Resize]bool{
		{Disk: 10}:         false,
		{Disk: 19, Cpu: 4}: false,
		{Disk: 20}:         true,
		{Disk: 50}:         true,
		{Ram: 2}:           true,
	} {
		err := checkResize(20, r)
		if ok != (err == nil) || (err != nil && exitCode(err) != exitUsage) {
			t.Errorf("%+v: %v", r, err)
		}
	}
	// Under the current disk and under the floor at once, the current disk is what the message names.
	if err := checkResize(10, zerops.Resize{Disk: 5}); err == nil || err.Error() != "the disk of a workspace only grows, it is 10 GiB now and --disk 5 is smaller" {
		t.Errorf("smaller than the current disk: %v", err)
	}
	if err := checkResize(0, zerops.Resize{Disk: 5}); err == nil || exitCode(err) != exitUsage || err.Error() != "workspace size: 5 GiB disk is under the floor of 10 GiB disk" {
		t.Errorf("under the floor with no current size: %v", err)
	}
}

// The confirmation and the result name the size the resize ends at, and a trimmed size is told apart.
func TestResizedSize(t *testing.T) {
	size := zerops.Size{CpuMode: zerops.CpuShared, Cpu: 2, Ram: 4, Disk: 10}
	target := resized(size, zerops.Resize{Ram: 8, CpuMode: zerops.CpuDedicated})
	if got := sizeString(target); got != "2 cpu (dedicated), 8 GiB RAM, 10 GiB disk" {
		t.Error(got)
	}
	if got := sizeString(zerops.Size{Cpu: 2, Ram: 0.5, Disk: 10}); got != "2 cpu, 0.5 GiB RAM, 10 GiB disk" {
		t.Error(got)
	}
	if trimmed(target, zerops.Resize{Ram: 8}) || !trimmed(size, zerops.Resize{Cpu: 24}) || !trimmed(size, zerops.Resize{CpuMode: zerops.CpuDedicated}) {
		t.Error("trimmed")
	}
}

// A workspace pointed at by URL and key has no Zerops service to start, stop, restart or resize: a usage error, before any login is needed.
func TestServiceWorkspaceRefusesDirect(t *testing.T) {
	t.Setenv(config.EnvConfigPath, t.TempDir()+"/config.json")
	t.Setenv(config.EnvWorkspace, "")
	t.Setenv(config.EnvWorkspaceUrl, "https://byhand.example")
	t.Setenv(config.EnvWorkspaceKey, "k")
	_, _, _, err := serviceWorkspace(t.Context(), "stop")
	if err == nil || exitCode(err) != exitUsage || !strings.Contains(err.Error(), "no service to stop") {
		t.Errorf("got %v", err)
	}
}

// The lifecycle commands through a fake public API: a stop of a running workspace asks for --yes, sends the PUT and waits for the process,
// a reload does not ask and refuses a stopped workspace, a stop of a stopped one and a resize to the current or the trimmed size change nothing,
// and a smaller disk fails before the API is called.
func TestWorkspaceLifecycleCommands(t *testing.T) {
	const size = `{"cpuMode": "SHARED", "minResource": {"cpuCoreCount": 2, "memoryGBytes": 4, "diskGBytes": 20}, "maxResource": {"cpuCoreCount": 2, "memoryGBytes": 4, "diskGBytes": 20}}`
	status, custom := "ACTIVE", size
	// process is the answer of GET /process/p1: a process status, or an HTTP status that answers an error.
	process := "FINISHED"
	var puts []string
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/rest/public/service-stack/s1":
			_, _ = fmt.Fprintf(w, `{"id": "s1", "name": "zbox", "status": %q, "currentAutoscaling": {"verticalAutoscaling": %s}, "customAutoscaling": {"verticalAutoscaling": %s}}`, status, size, custom)
		case r.Method == http.MethodGet && r.URL.Path == "/api/rest/public/process/p1":
			if code, err := strconv.Atoi(process); err == nil {
				w.WriteHeader(code)
				_, _ = fmt.Fprint(w, `{"error": {"code": "notAuthorized", "message": "Not authorized"}}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id": "p1", "actionName": "stack.stop", "status": %q}`, process)
		case r.Method == http.MethodPut:
			puts = append(puts, r.URL.Path)
			if strings.HasSuffix(r.URL.Path, "/autoscaling") {
				_ = json.NewDecoder(r.Body).Decode(&sent)
				_, _ = fmt.Fprint(w, `{"process": {"id": "p2", "status": "PENDING"}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"id": "p1", "status": "PENDING"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := `{"apiUrl": "` + srv.URL + `", "projectId": "p", "serviceId": "s1", "workspaces": [{"name": "zbox", "projectId": "p", "serviceId": "s1"}]}`
	if err := os.WriteFile(dir+"/config.json", []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, dir+"/config.json")
	t.Setenv(config.EnvToken, "t")
	t.Setenv(config.EnvWorkspace, "")
	t.Setenv(config.EnvWorkspaceUrl, "")
	t.Setenv(config.EnvWorkspaceKey, "")
	terminal := stdinTerminal
	stdinTerminal = false
	defer func() { stdinTerminal = terminal }()

	run := func(args ...string) (string, error) {
		t.Helper()
		flagYes, flagNoWait, flagJson, flagQuiet = false, false, false, false
		flagResizeCpu, flagResizeRam, flagResizeDisk, flagResizeCpuMode = 0, 0, 0, ""
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout := os.Stdout
		os.Stdout = w
		rootCmd.SetArgs(args)
		err = rootCmd.ExecuteContext(t.Context())
		os.Stdout = stdout
		_ = w.Close()
		out, _ := io.ReadAll(r)
		return string(out), err
	}

	if _, err := run("workspace", "stop"); err == nil || exitCode(err) != exitUsage || len(puts) != 0 {
		t.Errorf("stop without --yes: %v, puts %v", err, puts)
	}
	out, err := run("workspace", "stop", "--yes", "--no-wait")
	if err != nil || out != "Workspace zbox is stopping, Zerops process p1.\n" || !slices.Equal(puts, []string{"/api/rest/public/service-stack/s1/stop"}) {
		t.Errorf("stop: %q %v, puts %v", out, err, puts)
	}
	puts = nil
	if out, err = run("workspace", "stop", "--yes"); err != nil || !strings.HasPrefix(out, "Workspace zbox stopped in ") || len(puts) != 1 {
		t.Errorf("stop that waits: %q %v, puts %v", out, err, puts)
	}
	if out, err = run("workspace", "stop", "--yes", "--json"); err != nil || !strings.Contains(out, `"status": "FINISHED"`) {
		t.Errorf("stop that waits --json: %q %v", out, err)
	}
	process = "FAILED"
	if _, err = run("workspace", "stop", "--yes"); err == nil || exitCode(err) != exitFailed || !strings.Contains(err.Error(), "process failed: stack.stop FAILED") {
		t.Errorf("stop whose process fails: %v", err)
	}
	process = "401"
	if _, err = run("workspace", "stop", "--yes"); err == nil || exitCode(err) != exitUnauthorized || !strings.Contains(err.Error(), "p1") {
		t.Errorf("stop whose process read is refused: %v", err)
	}
	process, puts = "FINISHED", nil
	if out, err = run("workspace", "reload", "--no-wait"); err != nil || out != "Workspace zbox is reloading, Zerops process p1.\n" || !slices.Equal(puts, []string{"/api/rest/public/service-stack/s1/reload"}) {
		t.Errorf("reload without --yes: %q %v, puts %v", out, err, puts)
	}
	status, puts = "STOPPED", nil
	if _, err = run("workspace", "reload"); err == nil || exitCode(err) != exitUnavailable || !strings.Contains(err.Error(), "`zbox workspace start` starts it") || len(puts) != 0 {
		t.Errorf("reload of a stopped workspace: %v, puts %v", err, puts)
	}
	if out, err = run("workspace", "stop"); err != nil || out != "Workspace zbox is already stopped.\n" || len(puts) != 0 {
		t.Errorf("stop of a stopped workspace: %q %v, puts %v", out, err, puts)
	}
	if out, err = run("workspace", "resize", "--disk", "20", "--cpu-mode", "shared"); err != nil || !strings.Contains(out, "already has 2 cpu (shared), 4 GiB RAM, 20 GiB disk") || len(puts) != 0 {
		t.Errorf("resize to the current size: %q %v, puts %v", out, err, puts)
	}
	if _, err = run("workspace", "resize", "--disk", "5", "-y"); err == nil || exitCode(err) != exitUsage || !strings.Contains(err.Error(), "only grows, it is 20 GiB now") || len(puts) != 0 {
		t.Errorf("smaller disk: %v, puts %v", err, puts)
	}
	if _, err = run("workspace", "resize", "--disk", "30", "-y"); err == nil || exitCode(err) != exitUnavailable || len(puts) != 0 {
		t.Errorf("disk grow of a stopped workspace: %v, puts %v", err, puts)
	}
	custom = `{"cpuMode": "SHARED", "minResource": {"cpuCoreCount": 24, "memoryGBytes": 4, "diskGBytes": 30}, "maxResource": {"cpuCoreCount": 24, "memoryGBytes": 4, "diskGBytes": 30}}`
	if out, err = run("workspace", "resize", "--cpu", "24", "-y"); err != nil || !strings.Contains(out, "already has 2 cpu (shared)") || len(puts) != 0 {
		t.Errorf("resize to a size trimmed before: %q %v, puts %v", out, err, puts)
	}
	if _, err = run("workspace", "resize", "--disk", "25", "-y"); err == nil || exitCode(err) != exitUsage || !strings.Contains(err.Error(), "it is 30 GiB now") || len(puts) != 0 {
		t.Errorf("disk under the custom one: %v, puts %v", err, puts)
	}
	custom = size
	// The disk the workspace has is no grow, so a stopped workspace takes it.
	if out, err = run("workspace", "resize", "--ram", "8", "--disk", "20", "-y", "--no-wait", "--json"); err != nil || !slices.Equal(puts, []string{"/api/rest/public/service-stack/s1/autoscaling"}) {
		t.Fatalf("resize: %q %v, puts %v", out, err, puts)
	}
	if dig := sent["customAutoscaling"].(map[string]any)["verticalAutoscaling"].(map[string]any); dig["cpuMode"] != "SHARED" || dig["maxResource"].(map[string]any)["memoryGBytes"] != 8.0 || dig["maxResource"].(map[string]any)["diskGBytes"] != 20.0 {
		t.Errorf("resize body %v", sent)
	}
	var change workspaceChange
	if err := json.Unmarshal([]byte(out), &change); err != nil || change.ServiceId != "s1" || change.Process == nil || change.Process.Id.Native() != "p2" {
		t.Errorf("resize --json: %s %v", out, err)
	}
}

// Under --json an account without workspaces lists them as [], not null.
func TestWorkspaceListJsonEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/rest/public/user/info":
			_, _ = fmt.Fprint(w, `{"id": "u1", "email": "a@b.c", "fullName": "A", "clientUserList": [{"clientId": "c1", "status": "ACTIVE", "client": {"accountName": "One"}}]}`)
		case "/api/rest/public/client/c1/project":
			_, _ = fmt.Fprint(w, `{"count": 0, "total": 0, "limit": 100, "offset": 0, "list": []}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/config.json", []byte(`{"apiUrl": "`+srv.URL+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, dir+"/config.json")
	t.Setenv(config.EnvToken, "t")
	t.Setenv(config.EnvWorkspace, "")
	t.Setenv(config.EnvWorkspaceUrl, "")
	t.Setenv(config.EnvWorkspaceKey, "")
	flagJson, flagQuiet = false, false
	defer func() { flagJson = false }()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	rootCmd.SetArgs([]string{"workspace", "list", "--json"})
	err = rootCmd.ExecuteContext(t.Context())
	os.Stdout = stdout
	_ = w.Close()
	out, _ := io.ReadAll(r)
	var got map[string]json.RawMessage
	if err != nil || json.Unmarshal(out, &got) != nil || string(got["account"]) != "[]" || string(got["known"]) != "[]" {
		t.Errorf("%s %v", out, err)
	}
}
