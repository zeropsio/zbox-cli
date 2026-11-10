package zerops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/zeropsio/zerops-go/dto/output"
)

// A new project goes to the first organisation that allows it, or to the one named by id or name; a name shared by two needs the id.
func TestProjectOrganization(t *testing.T) {
	u := User{ClientId: "c2", Account: "Team", Organizations: []Organization{
		{Id: "c1", Name: "Viewer"},
		{Id: "c2", Name: "Team", CanCreateProjects: true},
		{Id: "c3", Name: "Twin", CanCreateProjects: true},
		{Id: "c4", Name: "twin", CanCreateProjects: true},
	}}
	for ref, want := range map[string]string{"": "c2", "c3": "c3", "team": "c2", "c4": "c4"} {
		if got, err := u.ProjectOrganization(ref); err != nil || got.Id != want {
			t.Errorf("%q -> %+v %v", ref, got, err)
		}
	}
	if _, err := u.ProjectOrganization("Viewer"); !errors.Is(err, ErrCreateProjects) {
		t.Errorf("read-only organisation: %v", err)
	}
	if _, err := u.ProjectOrganization("twin"); err == nil || !strings.Contains(err.Error(), "c3") || !strings.Contains(err.Error(), "c4") {
		t.Errorf("ambiguous name: %v", err)
	}
	if _, err := u.ProjectOrganization("nope"); err == nil || errors.Is(err, ErrCreateProjects) || !strings.Contains(err.Error(), "Viewer (c1)") {
		t.Errorf("unknown organisation: %v", err)
	}
	if _, err := (User{}).ProjectOrganization(""); !errors.Is(err, ErrCreateProjects) {
		t.Errorf("no organisation: %v", err)
	}
}

// Workspaces walks every project of every organisation and keeps the Box Workspace services with both names on each row.
// A project or organisation that answers forbidden or not-found is left out, the organisation is reported; a 5xx fails the whole list.
func TestWorkspaces(t *testing.T) {
	projects := map[string][]string{"c1": {"p1", "denied", "p2", "gone"}, "c2": {"p3"}, "c4": {"broken"}}
	services := map[string][]map[string]string{
		"p1": {{"id": "s1", "name": "zbox", "serviceStackTypeId": "zbox"}, {"id": "db", "name": "db", "serviceStackTypeId": "postgresql"}},
		"p2": {{"id": "s2", "name": "box2", "serviceStackTypeId": "zbox"}},
		"p3": {{"id": "s3", "name": "box3", "serviceStackTypeId": "zbox"}},
	}
	refusals := map[string]struct {
		status int
		code   string
	}{
		"/client/c3/project":            {http.StatusForbidden, "insufficientPermissions"},
		"/client/c5/project":            {http.StatusBadRequest, "clientNotFound"},
		"/project/denied/service-stack": {http.StatusForbidden, "insufficientPermissions"},
		"/project/gone/service-stack":   {http.StatusBadRequest, "projectNotFound"},
		"/project/broken/service-stack": {http.StatusInternalServerError, "internalServerError"},
	}
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if refusal, ok := refusals[strings.TrimPrefix(r.URL.Path, "/api/rest/public")]; ok {
			w.WriteHeader(refusal.status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": refusal.code, "message": refusal.code}})
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		const limit = 1
		var list []map[string]any
		var total int
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/rest/public/"), "/")
		switch {
		case len(parts) == 3 && parts[0] == "client" && parts[2] == "project":
			all := projects[parts[1]]
			total = len(all)
			for _, id := range all[offset:min(offset+limit, total)] {
				list = append(list, map[string]any{"id": id, "clientId": parts[1], "name": "name-" + id, "status": "ACTIVE"})
			}
		case len(parts) == 3 && parts[0] == "project" && parts[2] == "service-stack":
			all := services[parts[1]]
			total = len(all)
			for _, s := range all[offset:min(offset+limit, total)] {
				list = append(list, map[string]any{"id": s["id"], "projectId": parts[1], "name": s["name"], "serviceStackTypeId": s["serviceStackTypeId"], "status": "ACTIVE"})
			}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": len(list), "total": total, "limit": limit, "offset": offset, "list": list})
	}))
	defer srv.Close()

	orgs := []Organization{{Id: "c1", Name: "One"}, {Id: "c2", Name: "Two"}, {Id: "c3", Name: "Denied"}, {Id: "c5", Name: "Gone"}}
	got, skipped, err := New(srv.URL, "t").Workspaces(t.Context(), orgs)
	if err != nil {
		t.Fatal(err)
	}
	want := []WorkspaceStack{
		{ServiceId: "s1", Hostname: "zbox", ProjectId: "p1", ProjectName: "name-p1", OrganizationId: "c1", OrganizationName: "One", Status: "ACTIVE"},
		{ServiceId: "s2", Hostname: "box2", ProjectId: "p2", ProjectName: "name-p2", OrganizationId: "c1", OrganizationName: "One", Status: "ACTIVE"},
		{ServiceId: "s3", Hostname: "box3", ProjectId: "p3", ProjectName: "name-p3", OrganizationId: "c2", OrganizationName: "Two", Status: "ACTIVE"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v", got)
	}
	if !slices.Equal(skipped, []Organization{orgs[2], orgs[3]}) {
		t.Errorf("skipped %+v", skipped)
	}
	for _, call := range calls {
		if !strings.Contains(call, "limit=") {
			t.Errorf("unexpected call %s", call)
		}
	}

	got, skipped, err = New(srv.URL, "t").Workspaces(t.Context(), []Organization{{Id: "empty", Name: "Empty"}})
	if err != nil || got == nil || len(got) != 0 || len(skipped) != 0 {
		t.Errorf("an organisation without projects: %v %v %v", got, skipped, err)
	}

	_, _, err = New(srv.URL, "t").Workspaces(t.Context(), append(orgs, Organization{Id: "c4", Name: "Broken"}))
	if err == nil || !strings.Contains(err.Error(), "project name-broken in organisation Broken") {
		t.Errorf("a 5xx on one project: %v", err)
	}
}

// Start, stop, restart and reload are PUTs on the service through the SDK, each answering the process that does it.
func TestServiceLifecycle(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer t" {
			t.Errorf("%s without the token", r.URL.Path)
		}
		action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "p-" + action, "status": "PENDING", "actionName": "stack." + action})
	}))
	defer srv.Close()

	c := New(srv.URL, "t")
	for _, tc := range []struct {
		call func(*Client, context.Context, string) (output.Process, error)
		want string
	}{
		{(*Client).StartServiceStack, "start"},
		{(*Client).StopServiceStack, "stop"},
		{(*Client).RestartServiceStack, "restart"},
		{(*Client).ReloadServiceStack, "reload"},
	} {
		p, err := tc.call(c, t.Context(), "s1")
		if err != nil {
			t.Fatal(err)
		}
		if p.Id.Native() != "p-"+tc.want {
			t.Errorf("%s answered process %q", tc.want, p.Id.Native())
		}
	}
	want := []string{"PUT /api/rest/public/service-stack/s1/start", "PUT /api/rest/public/service-stack/s1/stop", "PUT /api/rest/public/service-stack/s1/restart", "PUT /api/rest/public/service-stack/s1/reload"}
	if !slices.Equal(calls, want) {
		t.Errorf("calls %v", calls)
	}
}

// The autoscaling PUT carries the body as built and answers the process, or none.
func TestAutoscale(t *testing.T) {
	var got map[string]any
	answer := `{"process": {"id": "p1", "status": "PENDING"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/rest/public/service-stack/s1/autoscaling" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()

	in, err := AutoscalingBody(stackOf(t, `{"customAutoscaling": {"verticalAutoscaling": {"cpuMode": "SHARED", "minResource": {"cpuCoreCount": 2, "memoryGBytes": 4, "diskGBytes": 10}, "maxResource": {"cpuCoreCount": 2, "memoryGBytes": 4, "diskGBytes": 10}}}}`), Resize{Ram: 8})
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(srv.URL, "t").Autoscale(t.Context(), "s1", in)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.Id.Native() != "p1" {
		t.Errorf("process %+v", p)
	}
	if ram := dig(got, "customAutoscaling", "verticalAutoscaling", "maxResource", "memoryGBytes"); ram != 8.0 {
		t.Errorf("sent max ram %v in %v", ram, got)
	}

	answer = `{"process": null}`
	if p, err = New(srv.URL, "t").Autoscale(t.Context(), "s1", in); err != nil || p != nil {
		t.Errorf("no process: %+v %v", p, err)
	}
}

// The body keeps the stored custom autoscaling and changes only what the resize names, min and max alike; the cpu mode stays unless asked.
func TestAutoscalingBody(t *testing.T) {
	const stored = `{"customAutoscaling": {"verticalAutoscaling": {"cpuMode": "SHARED", "swapEnabled": true,
		"minResource": {"cpuCoreCount": 2, "memoryGBytes": 4, "diskGBytes": 10}, "maxResource": {"cpuCoreCount": 2, "memoryGBytes": 4, "diskGBytes": 10},
		"minFreeResource": {"memoryGBytes": 0.5}}}}`
	for _, tc := range []struct {
		name   string
		stack  string
		resize Resize
		want   map[string]any
	}{
		{"ram only", stored, Resize{Ram: 8}, map[string]any{"cpuMode": "SHARED", "minCpu": 2.0, "maxCpu": 2.0, "minRam": 8.0, "maxRam": 8.0, "minDisk": 10.0, "maxDisk": 10.0, "swap": true, "minFreeRam": 0.5}},
		{"all three", stored, Resize{Cpu: 4, Ram: 16, Disk: 50}, map[string]any{"cpuMode": "SHARED", "minCpu": 4.0, "maxCpu": 4.0, "minRam": 16.0, "maxRam": 16.0, "minDisk": 50.0, "maxDisk": 50.0}},
		{"cpu mode", stored, Resize{CpuMode: CpuDedicated}, map[string]any{"cpuMode": "DEDICATED", "minCpu": 2.0, "maxCpu": 2.0, "minRam": 4.0}},
		{"nothing stored", `{}`, Resize{Disk: 20}, map[string]any{"cpuMode": nil, "minCpu": nil, "maxCpu": nil, "minRam": nil, "minDisk": 20.0, "maxDisk": 20.0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := AutoscalingBody(stackOf(t, tc.stack), tc.resize)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			var sent map[string]any
			if err := json.Unmarshal(raw, &sent); err != nil {
				t.Fatal(err)
			}
			va := []string{"customAutoscaling", "verticalAutoscaling"}
			paths := map[string][]string{
				"cpuMode": append(va, "cpuMode"), "swap": append(va, "swapEnabled"), "minFreeRam": append(va, "minFreeResource", "memoryGBytes"),
				"minCpu": append(va, "minResource", "cpuCoreCount"), "maxCpu": append(va, "maxResource", "cpuCoreCount"),
				"minRam": append(va, "minResource", "memoryGBytes"), "maxRam": append(va, "maxResource", "memoryGBytes"),
				"minDisk": append(va, "minResource", "diskGBytes"), "maxDisk": append(va, "maxResource", "diskGBytes"),
			}
			for key, want := range tc.want {
				if got := dig(sent, paths[key]...); got != want {
					t.Errorf("%s = %v, want %v in %s", key, got, want, raw)
				}
			}
			if sent["autoscalingProfileId"] != nil {
				t.Errorf("a profile the service does not have was sent: %s", raw)
			}
		})
	}
}

// A profile goes back with its overrides, since the endpoint drops one the body leaves out.
func TestAutoscalingBodyKeepsProfile(t *testing.T) {
	in, err := AutoscalingBody(stackOf(t, `{"autoscalingProfileId": "small", "autoscalingProfileOverrides": {"a": 1}}`), Resize{Cpu: 2})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(in)
	if !strings.Contains(string(raw), `"autoscalingProfileId":"small"`) || !strings.Contains(string(raw), `"autoscalingProfileOverrides":{"a":1}`) {
		t.Errorf("profile lost: %s", raw)
	}
}

// The size comes from the current autoscaling, the custom one stands in; Applied wants min and max both at the size asked for.
func TestStackSize(t *testing.T) {
	current := stackOf(t, `{"currentAutoscaling": {"verticalAutoscaling": {"cpuMode": "SHARED", "minResource": {"cpuCoreCount": 4, "memoryGBytes": 8, "diskGBytes": 50}, "maxResource": {"cpuCoreCount": 4, "memoryGBytes": 8, "diskGBytes": 50}}},
		"customAutoscaling": {"verticalAutoscaling": {"maxResource": {"cpuCoreCount": 24}}}}`)
	if got, want := StackSize(current), (Size{CpuMode: "SHARED", Cpu: 4, Ram: 8, Disk: 50}); got != want {
		t.Errorf("size %+v", got)
	}
	if got := StackSize(stackOf(t, `{"customAutoscaling": {"verticalAutoscaling": {"maxResource": {"cpuCoreCount": 3}}}}`)); got != (Size{Cpu: 3}) {
		t.Errorf("custom stand-in %+v", got)
	}
	if StackSize(stackOf(t, `{}`)).Known() {
		t.Error("an empty service has a size")
	}
	for r, want := range map[Resize]bool{
		{Cpu: 4}:                     true,
		{Cpu: 4, Ram: 8, Disk: 50}:   true,
		{CpuMode: CpuShared, Ram: 8}: true,
		{Ram: 16}:                    false,
		{CpuMode: CpuDedicated}:      false,
		{Disk: 60}:                   false,
		{Cpu: 4, Ram: 8, Disk: 50, CpuMode: CpuDedicated}: false,
	} {
		if got := Applied(current, r); got != want {
			t.Errorf("Applied(%+v) = %v", r, got)
		}
	}
	scaling := stackOf(t, `{"currentAutoscaling": {"verticalAutoscaling": {"minResource": {"cpuCoreCount": 2}, "maxResource": {"cpuCoreCount": 4}}}}`)
	if Applied(scaling, Resize{Cpu: 4}) {
		t.Error("a min under the max is not the size asked for")
	}
}

// A size trimmed to the account's limit is applied once the custom autoscaling pins it, and the disk floor is the larger of the two disks.
func TestTrimmedSize(t *testing.T) {
	trimmedStack := stackOf(t, `{"currentAutoscaling": {"verticalAutoscaling": {"minResource": {"cpuCoreCount": 8, "diskGBytes": 50}, "maxResource": {"cpuCoreCount": 8, "diskGBytes": 50}}},
		"customAutoscaling": {"verticalAutoscaling": {"minResource": {"cpuCoreCount": 24, "diskGBytes": 100}, "maxResource": {"cpuCoreCount": 24, "diskGBytes": 100}}}}`)
	for r, want := range map[Resize]bool{
		{Cpu: 24}:            true,
		{Cpu: 8}:             true,
		{Cpu: 24, Disk: 100}: true,
		{Cpu: 24, Disk: 50}:  false,
		{Cpu: 16}:            false,
	} {
		if got := Applied(trimmedStack, r); got != want {
			t.Errorf("Applied(%+v) = %v", r, got)
		}
	}
	if got := DiskFloor(trimmedStack); got != 100 {
		t.Errorf("disk floor %v", got)
	}
	if got := DiskFloor(stackOf(t, `{"currentAutoscaling": {"verticalAutoscaling": {"maxResource": {"diskGBytes": 50}}}}`)); got != 50 {
		t.Errorf("disk floor without a custom one %v", got)
	}
}

// processServer answers GET /process/{id} with the next of the given answers, the last one repeated, and counts the reads.
// An answer is a process status, or an HTTP status with an optional error code as `400:processNotFound`.
func processServer(t *testing.T, answers ...string) (*Client, *int) {
	t.Helper()
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := answers[min(reads, len(answers)-1)]
		reads++
		w.Header().Set("Content-Type", "application/json")
		statusText, code, _ := strings.Cut(answer, ":")
		if status, err := strconv.Atoi(statusText); err == nil {
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error": {"code": %q, "message": "m"}}`, code)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id": %q, "actionName": "stack.stop", "status": %q}`, strings.TrimPrefix(r.URL.Path, "/api/rest/public/process/"), answer)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "t")
	c.pollInterval = 0
	return c, &reads
}

// WaitProcesses ends with the process, gives up at once on a refused read and after maxPollFailures failed reads in a row; a good read resets the count.
func TestWaitProcesses(t *testing.T) {
	const pid = "0123456789abcdef0123456789abcdef"
	var seen []string
	c, reads := processServer(t, "PENDING", "500", "RUNNING", "FINISHED")
	if err := c.WaitProcesses(t.Context(), []string{pid}, func(p ProcessState) { seen = append(seen, p.Status) }); err != nil || *reads != 4 {
		t.Errorf("finished: %v after %d reads", err, *reads)
	}
	if !slices.Equal(seen, []string{"PENDING", "RUNNING", "FINISHED"}) {
		t.Errorf("changes %v", seen)
	}

	c, _ = processServer(t, "RUNNING", "FAILED")
	if err := c.WaitProcesses(t.Context(), []string{pid}, func(ProcessState) {}); err == nil || !strings.Contains(err.Error(), "process failed: stack.stop FAILED") {
		t.Errorf("failed: %v", err)
	}

	for _, status := range []string{"401", "403", "404", "400:processNotFound"} {
		c, reads = processServer(t, "RUNNING", status)
		err := c.WaitProcesses(t.Context(), []string{pid}, func(ProcessState) {})
		if err == nil || !strings.Contains(err.Error(), pid) || *reads != 2 {
			t.Errorf("%s: %v after %d reads", status, err, *reads)
		}
	}
	c, reads = processServer(t, "400")
	if err := c.WaitProcesses(t.Context(), []string{pid}, func(ProcessState) {}); err == nil || *reads != maxPollFailures {
		t.Errorf("a 400 that is no refusal: %v after %d reads", err, *reads)
	}
	answers := []string{"500", "500", "500", "500", "RUNNING", "500", "500", "500", "500", "FINISHED"}
	c, reads = processServer(t, answers...)
	if err := c.WaitProcesses(t.Context(), []string{pid}, func(ProcessState) {}); err != nil || *reads != len(answers) {
		t.Errorf("failures with a good read between: %v after %d reads", err, *reads)
	}
	c, reads = processServer(t, "RUNNING", "502")
	err := c.WaitProcesses(t.Context(), []string{pid}, func(ProcessState) {})
	if err == nil || !strings.Contains(err.Error(), pid) || !strings.Contains(err.Error(), "5 attempts in a row") || *reads != 1+maxPollFailures {
		t.Errorf("failures in a row: %v after %d reads", err, *reads)
	}
}

func stackOf(t *testing.T, raw string) output.ServiceStack {
	t.Helper()
	var s output.ServiceStack
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// dig walks nested JSON objects by key, nil when a step is missing.
func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}
