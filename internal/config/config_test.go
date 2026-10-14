package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// The GUI origin is empty unless stored, the API's consent page URL then stands; a stored guiUrl loses its trailing slash.
func TestEffectiveGuiUrl(t *testing.T) {
	if got := (Config{ApiUrl: "https://api.app-futon.zerops.dev"}).EffectiveGuiUrl(); got != "" {
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
	if w := stored.EffectiveWorkspace(); w.WorkspaceUrl != "https://from-env" || w.ApiKey != "env-key" || !w.Direct() {
		t.Errorf("env override %+v", w)
	}

	c, err := stored.Pin("bench")
	if err != nil || !c.Pinned() || stored.Pinned() {
		t.Fatal(err, c.Pinned(), stored.Pinned())
	}
	if w := c.EffectiveWorkspace(); w.WorkspaceUrl != "https://bench" || w.ApiKey != "k2" || c.ServiceId != "svc2" {
		t.Errorf("pinned %+v %s", w, c.ServiceId)
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
	if got := current(); got.WorkspaceUrl != "https://mine" || len(got.Workspaces) != 2 || got.find(Workspace{ServiceId: "svc2"}) >= 0 {
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

// A workspace of the account is told apart by its service id and needs no key: it is usable, reached through zproxy, and its minted access is saved into its entry, the flat fields when it is current, and the stored selection under a pin. One pointed at by hand is told apart by its URL.
func TestAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(EnvConfigPath, path)
	c := Config{}
	mine := Workspace{Name: "mine", ServiceId: "svc1", ProjectId: "p1"}
	if err := c.Assign(mine); err != nil {
		t.Fatal(err)
	}
	if !c.HasWorkspace() || c.EffectiveWorkspace().Direct() || !c.IsCurrent(mine) {
		t.Errorf("keyless account workspace not usable: %+v", c)
	}
	// The same service stored again, with a URL learnt later, replaces the entry rather than doubling it.
	withUrl := mine
	withUrl.WorkspaceUrl = "https://mine-8080.app"
	if err := c.Remember(withUrl); err != nil || len(c.Workspaces) != 1 || c.WorkspaceUrl != withUrl.WorkspaceUrl {
		t.Errorf("remember by identity: %v %+v", err, c.Workspaces)
	}
	if err := c.Remember(Workspace{Name: "byhand", WorkspaceUrl: "https://other", ApiKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveAccess(mine, Access{Url: "https://proxy/service-api", Token: "t1"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Access == nil || got.Access.Token != "t1" || got.Workspaces[0].Access == nil || got.Workspaces[0].Access.Token != "t1" || got.Workspaces[1].Access != nil {
		t.Errorf("access not saved where it belongs: %+v", got)
	}
	// Under a pin the access of the pinned workspace is saved into its entry and the selection stays.
	pinned, err := got.Pin("byhand")
	if err != nil {
		t.Fatal(err)
	}
	if err := pinned.SaveAccess(mine, Access{Url: "https://proxy/service-api", Token: "t2"}); err != nil {
		t.Fatal(err)
	}
	if got, _ = Load(); got.ServiceId != "svc1" || got.Access.Token != "t2" || got.Workspaces[0].Access.Token != "t2" {
		t.Errorf("pinned save: %+v", got)
	}
	if w, err := got.Lookup("svc1"); err != nil || w.Label() != "mine-8080.app" || (Workspace{ServiceId: "x"}).Label() != "x" {
		t.Errorf("lookup %v %+v", err, w)
	}
}

func TestCurrentName(t *testing.T) {
	c := Config{Workspaces: []Workspace{{Name: "mine", ServiceId: "svc1", WorkspaceUrl: "https://mine-8080.example"}}}
	c.setCurrent(c.Workspaces[0])
	if w := c.EffectiveWorkspace(); w.Name != "mine" {
		t.Errorf("listed workspace named %q", w.Name)
	}
	c.Workspaces = nil
	if w := c.EffectiveWorkspace(); w.Name != "mine-8080" {
		t.Errorf("unlisted workspace named %q", w.Name)
	}
}

func TestHalfEnvPair(t *testing.T) {
	t.Setenv(EnvWorkspaceUrl, "https://from-env")
	t.Setenv(EnvWorkspaceKey, "")
	if set, missing := HalfEnvPair(); set != EnvWorkspaceUrl || missing != EnvWorkspaceKey {
		t.Errorf("half pair %q %q", set, missing)
	}
	// The pair never takes the current workspace's key for the missing half.
	c := Config{ApiKey: "stale-key", WorkspaceUrl: "https://mine"}
	if w := c.EffectiveWorkspace(); w.ApiKey != "" || w.WorkspaceUrl != "https://from-env" {
		t.Errorf("half pair mixed with the current workspace: %+v", w)
	}
	t.Setenv(EnvWorkspaceKey, "k")
	if set, _ := HalfEnvPair(); set != "" {
		t.Errorf("whole pair reported half: %q", set)
	}
}

func TestOverrideNotSaved(t *testing.T) {
	t.Setenv(EnvConfigPath, filepath.Join(t.TempDir(), "config.json"))
	c := Config{ApiUrl: "https://stored", GuiUrl: "https://gui"}
	empty := ""
	c.Override("https://flag", &empty)
	if c.ApiUrl != "https://flag" || c.GuiUrl != "" {
		t.Fatalf("override not applied: %s %q", c.ApiUrl, c.GuiUrl)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	saved, err := Load()
	if err != nil || saved.ApiUrl != "https://stored" || saved.GuiUrl != "https://gui" {
		t.Errorf("one-run flags saved: %v %s %s", err, saved.ApiUrl, saved.GuiUrl)
	}
	c.AdoptOverrides()
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if saved, _ = Load(); saved.ApiUrl != "https://flag" || saved.GuiUrl != "" {
		t.Errorf("login did not adopt the flags: %s %q", saved.ApiUrl, saved.GuiUrl)
	}
}
