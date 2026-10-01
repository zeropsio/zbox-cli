// Package config persists the CLI state: the Zerops token and API URL, the workspace in use (its Zerops ids when `zbox setup` created it, and the API URL and key every orchestrator call needs) and the other known workspaces `zbox switch` moves between.
package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultApiUrl is the Zerops public API of the dev environment the CLI works against: futon since 2026-10-01 (alpha from 2026-09-22, futon before that). A stored apiUrl wins, `zbox login --public-api-url` moves a config over.
	DefaultApiUrl = "https://api.app-futon.zerops.dev"
	// DefaultGuiUrl is the Zerops GUI of the same environment, futon since 2026-10-01 (alpha before, the local dev server on localhost:1111 before that), which serves the zbox hand-over; the config's guiUrl or --gui-url point elsewhere. The GUI has to target the API of DefaultApiUrl, or the hand-over mints tokens for another environment.
	DefaultGuiUrl = "https://futon.devel.zerops.dev"
	// DefaultBaseImage is the image registered as the first base of a new workspace: the saved default sandbox image (ubuntu 26.04 with claude, gh, codex, python3, node 24 and cron) on the futon plumbing bucket since 2026-10-01 (the same bytes as the alpha one on 3wnbd-s3, which stays), which the orchestrator pulls and flattens from the tarball URL. A plain registry reference works as well.
	DefaultBaseImage = "https://grg.zerops.dev/575s1-s3/zbox-base.tar.gz"

	EnvConfigPath   = "ZBOX_CONFIG"
	EnvToken        = "ZBOX_TOKEN"
	EnvWorkspaceUrl = "ZBOX_WORKSPACE_URL"
	EnvWorkspaceKey = "ZBOX_API_KEY"
	// EnvWorkspace names a known workspace for one run, the env form of --workspace. Unlike the URL and key pair it carries no secret, the key is the stored one.
	EnvWorkspace = "ZBOX_WORKSPACE"
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

	// pinned is the stored selection while the flat fields show the workspace named for this run (Pin). Save writes it back, so an override never becomes the current workspace.
	pinned *Workspace
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
	// The receiver is a copy, so the caller keeps the pinned view while the file gets the stored selection.
	if c.pinned != nil {
		c.setCurrent(*c.pinned)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// Pin answers the config with the workspace named by ref in place of the current one, for one run: every reader of the flat fields works on it, and Save still writes the selection that was stored. The key is the stored one, so the workspace has to be known here.
func (c Config) Pin(ref string) (Config, error) {
	w, err := c.Lookup(ref)
	if err != nil {
		return c, err
	}
	if w.ApiKey == "" {
		return c, fmt.Errorf("no key is stored for workspace %s (%s), `zbox workspace authorize %s` or `zbox workspace use %s --api-key <key>` stores it", w.Name, w.Host(), cmp.Or(w.ServiceId, "<service-id>"), w.WorkspaceUrl)
	}
	if c.pinned == nil {
		c.pinned = &Workspace{ProjectId: c.ProjectId, ProjectName: c.ProjectName, ServiceId: c.ServiceId, WorkspaceUrl: c.WorkspaceUrl, ApiKey: c.ApiKey}
	}
	c.setCurrent(w)
	return c, nil
}

// Pinned says whether the flat fields show a workspace named for this run rather than the stored selection.
func (c Config) Pinned() bool {
	return c.pinned != nil
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

// EffectiveWorkspace lets the environment point one run at any workspace, for scripts and for sandboxes that hold the key of their own workspace. A pinned workspace is the more specific word and is not overridden.
func (c Config) EffectiveWorkspace() (url, key string) {
	url, key = c.WorkspaceUrl, c.ApiKey
	if c.pinned != nil {
		return url, key
	}
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

// Lookup resolves what the user typed: a name, an API URL or host, a service id, a project name or id, or a prefix of the name or host when nothing matches whole. A ref that fits several workspaces is refused with the candidates, never settled by the order of the list.
func (c Config) Lookup(ref string) (Workspace, error) {
	ref = strings.TrimSuffix(ref, "/")
	if ref == "" {
		return Workspace{}, errors.New("no workspace named, `zbox workspace ls` lists them")
	}
	var hits []Workspace
	for _, w := range c.Workspaces {
		if w.Name == ref || w.WorkspaceUrl == ref || w.Host() == ref || (w.ServiceId != "" && w.ServiceId == ref) || (w.ProjectName != "" && w.ProjectName == ref) || (w.ProjectId != "" && w.ProjectId == ref) {
			hits = append(hits, w)
		}
	}
	if len(hits) == 0 {
		for _, w := range c.Workspaces {
			if strings.HasPrefix(w.Host(), ref) || strings.HasPrefix(w.Name, ref) {
				hits = append(hits, w)
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Workspace{}, fmt.Errorf("no workspace with a key stored here matches %q: `zbox workspace ls` lists them, `zbox workspace authorize <service-id>` or `zbox workspace use <url> --api-key <key>` stores the key of one", ref)
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches several workspaces, name one by its name, service id or API host:", ref)
		for _, w := range hits {
			fmt.Fprintf(&b, "\n  %s  (project %s, service %s, %s)", w.Name, cmp.Or(w.ProjectName, "-"), cmp.Or(w.ServiceId, "-"), w.Host())
		}
		return Workspace{}, errors.New(b.String())
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

// ForgetWorkspace drops the current workspace from the list and leaves none in use, `zbox switch` picks the next one. A pinned workspace is dropped the same way and the stored selection stays, unless it was that very workspace.
func (c *Config) ForgetWorkspace() error {
	if i := c.find(c.WorkspaceUrl); i >= 0 {
		c.Workspaces = append(c.Workspaces[:i], c.Workspaces[i+1:]...)
	}
	var stays Workspace
	if c.pinned != nil && c.pinned.WorkspaceUrl != c.WorkspaceUrl {
		stays = *c.pinned
	}
	c.pinned = nil
	c.setCurrent(stays)
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
