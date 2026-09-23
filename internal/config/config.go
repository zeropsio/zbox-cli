// Package config persists the CLI state: the Zerops token and API URL, the workspace in use (its Zerops ids when `zbox setup` created it, and the API URL and key every orchestrator call needs) and the other known workspaces `zbox switch` moves between.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultApiUrl is the Zerops public API of the dev environment the CLI works against, alpha since 2026-09-22 (futon before). A stored apiUrl wins, `zbox login --public-api-url` moves a config over.
	DefaultApiUrl = "https://api.app-alpha.zerops.dev"
	// DefaultGuiUrl is the Zerops GUI of the alpha environment, which serves the zbox hand-over since 2026-09-22 (the local dev server on localhost:1111 before); the config's guiUrl or --gui-url point elsewhere.
	DefaultGuiUrl = "http://alpha.devel.zerops.dev"
	// DefaultBaseImage is the image registered as the first base of a new workspace: the saved default sandbox image on the alpha plumbing bucket since 2026-09-22 (ubuntu 26.04 with claude, gh, codex, python3, node 24 and cron; the futon bucket 57fhv-s3 still holds the older image), which the orchestrator pulls and flattens from the tarball URL. A plain registry reference works as well.
	DefaultBaseImage = "https://grg.zerops.dev/3wnbd-s3/zbox-base.tar.gz"

	EnvConfigPath   = "ZBOX_CONFIG"
	EnvToken        = "ZBOX_TOKEN"
	EnvWorkspaceUrl = "ZBOX_WORKSPACE_URL"
	EnvWorkspaceKey = "ZBOX_API_KEY"
)

type Config struct {
	ApiUrl string `json:"apiUrl"`
	// GuiUrl is the Zerops GUI origin the browser hand-offs open. Empty means DefaultGuiUrl; a `zbox login --gui-url` saves the flag here with the token.
	GuiUrl      string `json:"guiUrl,omitempty"`
	Token       string `json:"token,omitempty"`
	ClientId    string `json:"clientId,omitempty"`
	ProjectId   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	// ServiceId keeps its spelling from before the workspace rename so an existing assignment survives it.
	ServiceId string `json:"serviceId,omitempty"`
	// WorkspaceUrl is the API host of the workspace, the first of the two subdomains the service publishes. The ingress host comes from GET /workspace.
	WorkspaceUrl string `json:"workspaceUrl,omitempty"`
	// ApiKey is the workspace API key, the generated sensitive env of the service and the whole authority on the orchestrator API.
	ApiKey string `json:"apiKey,omitempty"`
	// Workspaces are the known workspaces, the current one included. The flat fields above stay the current one so every reader is unchanged; `zbox switch` copies another entry into them.
	Workspaces []Workspace `json:"workspaces,omitempty"`
}

// Workspace is one saved assignment, addressed by its name in `zbox switch`.
type Workspace struct {
	Name         string `json:"name"`
	ProjectId    string `json:"projectId,omitempty"`
	ProjectName  string `json:"projectName,omitempty"`
	ServiceId    string `json:"serviceId,omitempty"`
	WorkspaceUrl string `json:"workspaceUrl"`
	ApiKey       string `json:"apiKey"`
}

// Host is the API host without the scheme, what the table shows and what `zbox switch` matches on.
func (w Workspace) Host() string {
	if u, err := url.Parse(w.WorkspaceUrl); err == nil && u.Host != "" {
		return u.Host
	}
	return w.WorkspaceUrl
}

// DefaultName names a workspace assigned without a name after the first label of its API host, `zbox-c-8080` for `https://zbox-c-8080.app-futon.zerops.dev`.
func DefaultName(workspaceUrl string) string {
	host := Workspace{WorkspaceUrl: workspaceUrl}.Host()
	if i := strings.Index(host, "."); i > 0 {
		return host[:i]
	}
	return host
}

func Path() (string, error) {
	if p := os.Getenv(EnvConfigPath); p != "" {
		return p, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "zbox", "config.json"), nil
}

func Load() (Config, error) {
	c := Config{ApiUrl: DefaultApiUrl}
	p, err := Path()
	if err != nil {
		return c, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if c.ApiUrl == "" {
		c.ApiUrl = DefaultApiUrl
	}
	// A config from before the list carries only the flat fields: it becomes the one known workspace, named after its project or host.
	if c.WorkspaceUrl != "" && c.find(c.WorkspaceUrl) < 0 {
		name := c.ProjectName
		if name == "" {
			name = DefaultName(c.WorkspaceUrl)
		}
		c.Workspaces = append(c.Workspaces, Workspace{Name: name, ProjectId: c.ProjectId, ProjectName: c.ProjectName, ServiceId: c.ServiceId, WorkspaceUrl: c.WorkspaceUrl, ApiKey: c.ApiKey})
	}
	return c, nil
}

func (c Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// EffectiveGuiUrl is the Zerops GUI origin for the browser hand-offs: the stored value, otherwise the development default.
func (c Config) EffectiveGuiUrl() string {
	if c.GuiUrl != "" {
		return strings.TrimSuffix(c.GuiUrl, "/")
	}
	return DefaultGuiUrl
}

// EffectiveToken lets ZBOX_TOKEN override the stored token, which keeps CI and scripts stateless.
func (c Config) EffectiveToken() string {
	if t := os.Getenv(EnvToken); t != "" {
		return t
	}
	return c.Token
}

// EffectiveWorkspace lets the environment point one run at any workspace, for scripts and for sandboxes that hold the key of their own workspace.
func (c Config) EffectiveWorkspace() (url, key string) {
	url, key = c.WorkspaceUrl, c.ApiKey
	if v := os.Getenv(EnvWorkspaceUrl); v != "" {
		url = v
	}
	if v := os.Getenv(EnvWorkspaceKey); v != "" {
		key = v
	}
	return url, key
}

// HasWorkspace says whether an orchestrator can be called at all. The Zerops ids are optional: a workspace assigned with `zbox workspace use` has none.
func (c Config) HasWorkspace() bool {
	url, key := c.EffectiveWorkspace()
	return url != "" && key != ""
}

// HasZeropsWorkspace says whether the workspace was created by this CLI, which is what teardown and status need.
func (c Config) HasZeropsWorkspace() bool {
	return c.ProjectId != "" && c.ServiceId != ""
}

// Current is the workspace in use, as an entry of the list.
func (c Config) Current() (Workspace, bool) {
	if c.WorkspaceUrl == "" {
		return Workspace{}, false
	}
	if i := c.find(c.WorkspaceUrl); i >= 0 {
		return c.Workspaces[i], true
	}
	return Workspace{Name: DefaultName(c.WorkspaceUrl), ProjectId: c.ProjectId, ProjectName: c.ProjectName, ServiceId: c.ServiceId, WorkspaceUrl: c.WorkspaceUrl, ApiKey: c.ApiKey}, true
}

// find returns the index of the entry with the URL, -1 when unknown.
func (c Config) find(workspaceUrl string) int {
	for i, w := range c.Workspaces {
		if w.WorkspaceUrl == workspaceUrl {
			return i
		}
	}
	return -1
}

// Lookup resolves what the user typed: a name, an API URL or host, a host prefix, or a service id.
func (c Config) Lookup(ref string) (Workspace, error) {
	ref = strings.TrimSuffix(ref, "/")
	for _, w := range c.Workspaces {
		if w.Name == ref || w.WorkspaceUrl == ref || w.Host() == ref || (w.ServiceId != "" && w.ServiceId == ref) {
			return w, nil
		}
	}
	var hits []Workspace
	for _, w := range c.Workspaces {
		if strings.HasPrefix(w.Host(), ref) || strings.HasPrefix(w.Name, ref) {
			hits = append(hits, w)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Workspace{}, fmt.Errorf("no known workspace matches %q, `zbox workspace ls` lists them", ref)
	default:
		names := make([]string, len(hits))
		for i, w := range hits {
			names[i] = w.Name
		}
		return Workspace{}, fmt.Errorf("%q matches several workspaces: %s", ref, strings.Join(names, ", "))
	}
}

// Remember stores w in the list without switching to it, unless nothing is current yet. A name already used by another workspace is refused rather than silently reassigned.
func (c *Config) Remember(w Workspace) error {
	if w.Name == "" {
		w.Name = DefaultName(w.WorkspaceUrl)
	}
	for _, known := range c.Workspaces {
		if known.Name == w.Name && known.WorkspaceUrl != w.WorkspaceUrl {
			return fmt.Errorf("the name %s already belongs to workspace %s, pick another with --name or forget that one first", w.Name, known.Host())
		}
	}
	if i := c.find(w.WorkspaceUrl); i >= 0 {
		c.Workspaces[i] = w
	} else {
		c.Workspaces = append(c.Workspaces, w)
	}
	if c.WorkspaceUrl == "" || c.WorkspaceUrl == w.WorkspaceUrl {
		c.setCurrent(w)
	}
	return c.Save()
}

// Assign remembers w and makes it the current workspace.
func (c *Config) Assign(w Workspace) error {
	if err := c.Remember(w); err != nil {
		return err
	}
	c.setCurrent(w)
	return c.Save()
}

func (c *Config) setCurrent(w Workspace) {
	c.ProjectId, c.ProjectName, c.ServiceId, c.WorkspaceUrl, c.ApiKey = w.ProjectId, w.ProjectName, w.ServiceId, w.WorkspaceUrl, w.ApiKey
}

// ForgetWorkspace drops the current workspace from the list and leaves none in use, `zbox switch` picks the next one.
func (c *Config) ForgetWorkspace() error {
	if i := c.find(c.WorkspaceUrl); i >= 0 {
		c.Workspaces = append(c.Workspaces[:i], c.Workspaces[i+1:]...)
	}
	c.ProjectId, c.ProjectName, c.ServiceId, c.WorkspaceUrl, c.ApiKey = "", "", "", "", ""
	return c.Save()
}

// Others lists the known workspaces that are not the current one, for the hint after a teardown or forget.
func (c Config) Others() []string {
	var names []string
	for _, w := range c.Workspaces {
		if w.WorkspaceUrl != c.WorkspaceUrl {
			names = append(names, w.Name)
		}
	}
	return names
}
