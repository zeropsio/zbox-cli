package config

import "testing"

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
