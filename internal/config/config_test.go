package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// The GUI origin is the development default unless stored; a stored guiUrl loses its trailing slash.
func TestEffectiveGuiUrl(t *testing.T) {
	if got := (Config{ApiUrl: "https://api.app-futon.zerops.dev"}).EffectiveGuiUrl(); got != DefaultGuiUrl {
		t.Error(got)
	}
	if got := (Config{ApiUrl: "x", GuiUrl: "https://g/"}).EffectiveGuiUrl(); got != "https://g" {
		t.Error(got)
	}
}

// Switching resolves names, hosts, prefixes and service ids, refuses a name that belongs to another workspace, and forget names the others.
func TestWorkspaces(t *testing.T) {
	c := Config{Workspaces: []Workspace{
		{Name: "futon", WorkspaceUrl: "https://zbox-c-8080.app-futon.zerops.dev", ServiceId: "svc1"},
		{Name: "local", WorkspaceUrl: "http://127.0.0.1:18080"},
	}}
	for _, ref := range []string{"futon", "zbox-c", "zbox-c-8080.app-futon.zerops.dev", "svc1", "https://zbox-c-8080.app-futon.zerops.dev/"} {
		if w, err := c.Lookup(ref); err != nil || w.Name != "futon" {
			t.Errorf("%s -> %v %v", ref, w.Name, err)
		}
	}
	if _, err := c.Lookup("nope"); err == nil {
		t.Error("unknown ref accepted")
	}
	if DefaultName("https://zbox-c-8080.app-futon.zerops.dev") != "zbox-c-8080" {
		t.Error("default name")
	}
	if got := (Config{WorkspaceUrl: "http://127.0.0.1:18080", Workspaces: c.Workspaces}).Others(); len(got) != 1 || got[0] != "futon" {
		t.Errorf("others %v", got)
	}
}

// A ref that fits several workspaces is refused with the candidates, whole matches come before prefixes, and the project is a way to name a workspace only while it holds one.
func TestLookupAmbiguous(t *testing.T) {
	c := Config{Workspaces: []Workspace{
		{Name: "zbox", ProjectName: "zbox-demo", ProjectId: "p1", ServiceId: "svc1", WorkspaceUrl: "https://zbox-30-8080.app-alpha.zerops.dev", ApiKey: "k1"},
		{Name: "zboxbench", ProjectName: "zbox-matej", ProjectId: "p2", ServiceId: "svc2", WorkspaceUrl: "https://zboxbench-1a-8080.app-alpha.zerops.dev", ApiKey: "k2"},
		{Name: "matej", ProjectName: "zbox-matej", ProjectId: "p2", ServiceId: "svc3", WorkspaceUrl: "https://zbox-1a-8080.app-alpha.zerops.dev", ApiKey: "k3"},
	}}
	for ref, name := range map[string]string{"zbox": "zbox", "zboxb": "zboxbench", "svc3": "matej", "zbox-demo": "zbox", "p1": "zbox", "zboxbench-1a": "zboxbench"} {
		if w, err := c.Lookup(ref); err != nil || w.Name != name {
			t.Errorf("%s -> %v %v", ref, w.Name, err)
		}
	}
	for ref, candidates := range map[string][]string{"zbox-matej": {"svc2", "svc3"}, "p2": {"svc2", "svc3"}, "zbox-": {"svc1", "svc3"}} {
		_, err := c.Lookup(ref)
		if err == nil || !strings.Contains(err.Error(), "several") || !strings.Contains(err.Error(), candidates[0]) || !strings.Contains(err.Error(), candidates[1]) {
			t.Errorf("%s -> %v", ref, err)
		}
	}
	if _, err := c.Lookup(""); err == nil {
		t.Error("empty ref accepted")
	}
}

// A pinned workspace stands in for the current one and is never what gets saved, not even when it is forgotten.
func TestPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvWorkspaceUrl, "https://from-env")
	t.Setenv(EnvWorkspaceKey, "env-key")
	stored := Config{Workspaces: []Workspace{
		{Name: "mine", ServiceId: "svc1", WorkspaceUrl: "https://mine", ApiKey: "k1"},
		{Name: "bench", ServiceId: "svc2", WorkspaceUrl: "https://bench", ApiKey: "k2"},
		{Name: "keyless", WorkspaceUrl: "https://keyless"},
	}}
	stored.setCurrent(stored.Workspaces[0])
	if err := stored.Save(); err != nil {
		t.Fatal(err)
	}
	if url, key := stored.EffectiveWorkspace(); url != "https://from-env" || key != "env-key" {
		t.Errorf("env override %s %s", url, key)
	}

	c, err := stored.Pin("bench")
	if err != nil || !c.Pinned() || stored.Pinned() {
		t.Fatal(err, c.Pinned(), stored.Pinned())
	}
	if url, key := c.EffectiveWorkspace(); url != "https://bench" || key != "k2" || c.ServiceId != "svc2" {
		t.Errorf("pinned %s %s %s", url, key, c.ServiceId)
	}
	if _, err := stored.Pin("keyless"); err == nil || !strings.Contains(err.Error(), "authorize") {
		t.Errorf("keyless pinned: %v", err)
	}
	if _, err := stored.Pin("nope"); err == nil {
		t.Error("unknown pinned")
	}

	current := func() Config {
		t.Helper()
		got, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if got := current(); got.WorkspaceUrl != "https://mine" || got.ApiKey != "k1" || got.ServiceId != "svc1" {
		t.Errorf("saved selection %+v", got)
	}
	if err := c.ForgetWorkspace(); err != nil {
		t.Fatal(err)
	}
	if c.Pinned() || c.WorkspaceUrl != "https://mine" {
		t.Errorf("after forget %v %s", c.Pinned(), c.WorkspaceUrl)
	}
	if got := current(); got.WorkspaceUrl != "https://mine" || len(got.Workspaces) != 2 || got.find("https://bench") >= 0 {
		t.Errorf("forgot the wrong one %+v", got.Workspaces)
	}

	// Pinning the current one and forgetting it leaves none in use.
	c, err = current().Pin("mine")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ForgetWorkspace(); err != nil {
		t.Fatal(err)
	}
	if got := current(); got.WorkspaceUrl != "" || got.ApiKey != "" || len(got.Workspaces) != 1 {
		t.Errorf("current survived its forget %+v", got)
	}
}
