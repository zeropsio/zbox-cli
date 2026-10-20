// Package config persists the CLI state: the Zerops token and API URL, the workspace in use (its Zerops ids when the account knows it, the zproxy access minted for it, or the API URL and key of one pointed at by hand) and the other known workspaces `zbox workspace switch` moves between.
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
	"time"
)

const (
	// DefaultApiUrl is the Zerops public API of the dev environment the CLI works against: futon since 2026-10-01 (alpha from 2026-09-22, futon before that). A stored apiUrl wins, `zbox login --public-api-url` moves a config over.
	DefaultApiUrl = "https://api.app-futon.zerops.dev"
	// DefaultBaseImage is the image registered as the first base of a new workspace: the saved default sandbox image (ubuntu 26.04 with claude, gh, codex, python3, node 24 and cron) on the production Box storage bucket through the Zerops CDN since 2026-10-20 (the plumbing bucket from 2026-10-12, the same bytes as the futon and alpha copies before it), which the orchestrator pulls and flattens from the tarball URL. A plain registry reference works as well.
	DefaultBaseImage = "https://storage.cdn.zerops.app/4ga3k-boxstorage/zbox/zbox-base.tar.gz"

	EnvConfigPath   = "ZBOX_CONFIG"
	EnvToken        = "ZBOX_TOKEN"
	EnvWorkspaceUrl = "ZBOX_WORKSPACE_URL"
	EnvWorkspaceKey = "ZBOX_API_KEY"
	// EnvWorkspace names a known workspace for one run, the env form of --workspace. Unlike the URL and key pair it carries no secret, the access is the stored one.
	EnvWorkspace = "ZBOX_WORKSPACE"
)

type Config struct {
	ApiUrl string `json:"apiUrl"`
	// GuiUrl is a Zerops GUI origin that stands in for the one the API names in the consent page URL of a browser login, for the GUI dev server. Empty keeps the API's; a `zbox login --gui-url` saves the flag here with the login.
	GuiUrl string `json:"guiUrl,omitempty"`
	// Session is the login of `zbox login` and `zbox register`, refreshed by the CLI. Token is a personal access token given to `zbox login --token`, stored as is; a login sets one of the two.
	Session     *Session `json:"session,omitempty"`
	Token       string   `json:"token,omitempty"`
	ClientId    string   `json:"clientId,omitempty"`
	ProjectId   string   `json:"projectId,omitempty"`
	ProjectName string   `json:"projectName,omitempty"`
	// ServiceId keeps its spelling from before the workspace rename so an existing assignment survives it.
	ServiceId string `json:"serviceId,omitempty"`
	// WorkspaceUrl is the API host of the workspace, the first of the two subdomains the service publishes, empty on a workspace without subdomain access. The ingress host comes from GET /workspace.
	WorkspaceUrl string `json:"workspaceUrl,omitempty"`
	// ApiKey is the workspace API key of a workspace pointed at by hand (`zbox workspace use --api-key`), the generated sensitive env of the service and the whole authority on the orchestrator API. A workspace of the account carries none here, zproxy holds it.
	ApiKey string `json:"apiKey,omitempty"`
	// Access is the zproxy access of the workspace in use, see Workspace.Access.
	Access *Access `json:"access,omitempty"`
	// Workspaces are the known workspaces, the current one included. The flat fields above stay the current one so every reader is unchanged; `zbox workspace switch` copies another entry into them.
	Workspaces []Workspace `json:"workspaces,omitempty"`

	// pinned is the stored selection while the flat fields show the workspace named for this run (Pin). Save writes it back, so an override never becomes the current workspace.
	pinned *Workspace
	// stored holds the apiUrl and guiUrl the file had when a run overrode them with the hidden flags; Save writes them back, so a one-run flag never lands in the file. AdoptOverrides drops it, which is what a login does since storing them is its job.
	stored *storedUrls
}

// Workspace is one saved assignment, addressed by its name in `zbox workspace switch`. A workspace of the account has a ServiceId and is reached through zproxy with an access minted from the Zerops token; one pointed at by hand has the API URL and the key.
type Workspace struct {
	Name         string  `json:"name"`
	ProjectId    string  `json:"projectId,omitempty"`
	ProjectName  string  `json:"projectName,omitempty"`
	ServiceId    string  `json:"serviceId,omitempty"`
	WorkspaceUrl string  `json:"workspaceUrl,omitempty"`
	ApiKey       string  `json:"apiKey,omitempty"`
	Access       *Access `json:"access,omitempty"`
}

// Session is the Zerops login: the access token every public API call sends, when it runs out, and the refresh token that gets the next pair.
type Session struct {
	AccessToken  string    `json:"accessToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	RefreshToken string    `json:"refreshToken"`
}

// Access is the zproxy access of a workspace: the base URL the orchestrator API is reached under and the access token sent as the bearer, which zproxy swaps for the workspace key. Minted with `POST /service-stack/{id}/ui-access` of the public API, it lives an hour past its last use and is minted again when zproxy refuses it.
type Access struct {
	Url   string `json:"url"`
	Token string `json:"token"`
}

// Host is the API host without the scheme, what the table shows and what `zbox workspace switch` matches on. Empty on a workspace without subdomain access.
func (w Workspace) Host() string {
	if u, err := url.Parse(w.WorkspaceUrl); err == nil && u.Host != "" {
		return u.Host
	}
	return w.WorkspaceUrl
}

// Direct says whether the workspace is reached at its own API host with the key, the way `zbox workspace use --api-key` stores one, rather than through zproxy with the account.
func (w Workspace) Direct() bool {
	return w.ServiceId == ""
}

// Usable says whether the orchestrator of the workspace can be called at all: a workspace of the account (zproxy mints the access), or a URL and key pair.
func (w Workspace) Usable() bool {
	return !w.Direct() || (w.WorkspaceUrl != "" && w.ApiKey != "")
}

// Label is the workspace in a sentence: the API host when it has one, the service id otherwise.
func (w Workspace) Label() string {
	return cmp.Or(w.Host(), w.ServiceId)
}

// identity is what tells two entries apart: the Zerops service for a workspace of the account, the API URL for one pointed at by hand.
func (w Workspace) identity() string {
	if w.ServiceId != "" {
		return "service:" + w.ServiceId
	}
	return "url:" + w.WorkspaceUrl
}

// Same says whether two entries are the same workspace.
func (w Workspace) Same(o Workspace) bool {
	return w.identity() == o.identity() && (w.ServiceId != "" || w.WorkspaceUrl != "")
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
	if cur := c.current(); cur.Usable() && c.find(cur) < 0 {
		cur.Name = cmp.Or(c.ProjectName, DefaultName(c.WorkspaceUrl), c.ServiceId)
		c.Workspaces = append(c.Workspaces, cur)
	}
	return c, nil
}

type storedUrls struct{ apiUrl, guiUrl string }

// Override replaces the API and GUI URLs for this run only (a nil guiUrl leaves it), Save keeps writing the stored ones.
func (c *Config) Override(apiUrl string, guiUrl *string) {
	if apiUrl == "" && guiUrl == nil {
		return
	}
	if c.stored == nil {
		c.stored = &storedUrls{apiUrl: c.ApiUrl, guiUrl: c.GuiUrl}
	}
	if apiUrl != "" {
		c.ApiUrl = apiUrl
	}
	if guiUrl != nil {
		c.GuiUrl = *guiUrl
	}
}

// AdoptOverrides makes the overridden URLs the ones Save writes, for the login that moves a config between environments.
func (c *Config) AdoptOverrides() { c.stored = nil }

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
	if c.stored != nil {
		c.ApiUrl, c.GuiUrl = c.stored.apiUrl, c.stored.guiUrl
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// Pin answers the config with the workspace named by ref in place of the current one, for one run: every reader of the flat fields works on it, and Save still writes the selection that was stored.
func (c Config) Pin(ref string) (Config, error) {
	w, err := c.Lookup(ref)
	if err != nil {
		return c, err
	}
	if !w.Usable() {
		return c, fmt.Errorf("no access is stored for workspace %s (%s), `zbox workspace authorize %s` or `zbox workspace use %s --api-key <key>` stores it", w.Name, w.Label(), cmp.Or(w.ServiceId, "<service-id>"), cmp.Or(w.WorkspaceUrl, "<url>"))
	}
	if c.pinned == nil {
		cur := c.current()
		c.pinned = &cur
	}
	c.setCurrent(w)
	return c, nil
}

// Pinned says whether the flat fields show a workspace named for this run rather than the stored selection.
func (c Config) Pinned() bool {
	return c.pinned != nil
}

// EffectiveGuiUrl is the stored GUI origin without its trailing slash, empty when the API's consent page URL is to be used as it is.
func (c Config) EffectiveGuiUrl() string {
	return strings.TrimSuffix(c.GuiUrl, "/")
}

// EffectiveToken is the bearer of the public API calls: ZBOX_TOKEN overrides the stored login, which keeps CI and scripts stateless.
func (c Config) EffectiveToken() string {
	if t := os.Getenv(EnvToken); t != "" {
		return t
	}
	if c.Session != nil {
		return c.Session.AccessToken
	}
	return c.Token
}

// EffectiveWorkspace is the workspace one run works on: the current (or pinned) one, or the direct URL and key pair the environment names, for scripts and for sandboxes that hold the key of their own workspace. A pinned workspace is the more specific word and is not overridden. The environment pair stands on its own: it never mixes with the current workspace, whose stored key may be the stale one of an account workspace from before zproxy (HalfEnvPair refuses half a pair before any reader gets here).
func (c Config) EffectiveWorkspace() Workspace {
	w := c.current()
	if c.pinned != nil {
		return w
	}
	envUrl, envKey := os.Getenv(EnvWorkspaceUrl), os.Getenv(EnvWorkspaceKey)
	if envUrl == "" && envKey == "" {
		return w
	}
	direct := Workspace{WorkspaceUrl: envUrl, ApiKey: envKey}
	direct.Name = DefaultName(direct.WorkspaceUrl)
	return direct
}

// HalfEnvPair names the one of ZBOX_WORKSPACE_URL and ZBOX_API_KEY that is set without the other, empty when both or neither are. The pair only stands as a whole.
func HalfEnvPair() (set, missing string) {
	envUrl, envKey := os.Getenv(EnvWorkspaceUrl), os.Getenv(EnvWorkspaceKey)
	switch {
	case envUrl != "" && envKey == "":
		return EnvWorkspaceUrl, EnvWorkspaceKey
	case envKey != "" && envUrl == "":
		return EnvWorkspaceKey, EnvWorkspaceUrl
	}
	return "", ""
}

// HasWorkspace says whether an orchestrator can be called at all.
func (c Config) HasWorkspace() bool {
	return c.EffectiveWorkspace().Usable()
}

// HasZeropsWorkspace says whether the workspace is a service of the account, which is what teardown and status need.
func (c Config) HasZeropsWorkspace() bool {
	return c.ProjectId != "" && c.ServiceId != ""
}

// current is the flat fields as an entry, named after the list entry it matches, or by default when it is not in the list.
func (c Config) current() Workspace {
	w := Workspace{ProjectId: c.ProjectId, ProjectName: c.ProjectName, ServiceId: c.ServiceId, WorkspaceUrl: c.WorkspaceUrl, ApiKey: c.ApiKey, Access: c.Access}
	if i := c.find(w); i >= 0 {
		w.Name = c.Workspaces[i].Name
	} else {
		w.Name = cmp.Or(DefaultName(c.WorkspaceUrl), c.ServiceId)
	}
	return w
}

// Current is the workspace in use, as an entry of the list.
func (c Config) Current() (Workspace, bool) {
	cur := c.current()
	if !cur.Usable() {
		return Workspace{}, false
	}
	if i := c.find(cur); i >= 0 {
		return c.Workspaces[i], true
	}
	return cur, true
}

// IsCurrent says whether w is the workspace the flat fields show.
func (c Config) IsCurrent(w Workspace) bool {
	return c.current().Same(w)
}

// find returns the index of the entry that is the same workspace, -1 when unknown.
func (c Config) find(w Workspace) int {
	for i, known := range c.Workspaces {
		if known.Same(w) {
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
		if w.Name == ref || (w.WorkspaceUrl != "" && (w.WorkspaceUrl == ref || w.Host() == ref)) || (w.ServiceId != "" && w.ServiceId == ref) || (w.ProjectName != "" && w.ProjectName == ref) || (w.ProjectId != "" && w.ProjectId == ref) {
			hits = append(hits, w)
		}
	}
	if len(hits) == 0 {
		for _, w := range c.Workspaces {
			if (w.Host() != "" && strings.HasPrefix(w.Host(), ref)) || strings.HasPrefix(w.Name, ref) {
				hits = append(hits, w)
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Workspace{}, fmt.Errorf("no workspace known here matches %q: `zbox workspace ls` lists them, `zbox workspace authorize <service-id>` stores one of the account and `zbox workspace use <url> --api-key <key>` any other", ref)
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches several workspaces, name one by its name, service id or API host:", ref)
		for _, w := range hits {
			fmt.Fprintf(&b, "\n  %s  (project %s, service %s, %s)", w.Name, cmp.Or(w.ProjectName, "-"), cmp.Or(w.ServiceId, "-"), cmp.Or(w.Host(), "-"))
		}
		return Workspace{}, errors.New(b.String())
	}
}

// Remember stores w in the list without switching to it, unless nothing is current yet. A name already used by another workspace is refused rather than silently reassigned.
func (c *Config) Remember(w Workspace) error {
	if w.Name == "" {
		w.Name = cmp.Or(DefaultName(w.WorkspaceUrl), w.ServiceId)
	}
	for _, known := range c.Workspaces {
		if known.Name == w.Name && !known.Same(w) {
			return fmt.Errorf("the name %s already belongs to workspace %s, pick another with --name or forget that one first", w.Name, known.Label())
		}
	}
	if i := c.find(w); i >= 0 {
		c.Workspaces[i] = w
	} else {
		c.Workspaces = append(c.Workspaces, w)
	}
	if !c.current().Usable() || c.IsCurrent(w) {
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

// SaveAccess stores a freshly minted zproxy access of w: in its list entry, in the flat fields when w is the one in use, and in the stored selection when that is w too. Nothing else about the selection changes.
func (c *Config) SaveAccess(w Workspace, a Access) error {
	if i := c.find(w); i >= 0 {
		c.Workspaces[i].Access = &a
	}
	if c.IsCurrent(w) {
		c.Access = &a
	}
	if c.pinned != nil && c.pinned.Same(w) {
		c.pinned.Access = &a
	}
	return c.Save()
}

func (c *Config) setCurrent(w Workspace) {
	c.ProjectId, c.ProjectName, c.ServiceId, c.WorkspaceUrl, c.ApiKey, c.Access = w.ProjectId, w.ProjectName, w.ServiceId, w.WorkspaceUrl, w.ApiKey, w.Access
}

// ForgetWorkspace drops the current workspace from the list and leaves none in use, `zbox workspace switch` picks the next one. A pinned workspace is dropped the same way and the stored selection stays, unless it was that very workspace.
func (c *Config) ForgetWorkspace() error {
	cur := c.current()
	if i := c.find(cur); i >= 0 {
		c.Workspaces = append(c.Workspaces[:i], c.Workspaces[i+1:]...)
	}
	var stays Workspace
	if c.pinned != nil && !c.pinned.Same(cur) {
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
		if !c.IsCurrent(w) {
			names = append(names, w.Name)
		}
	}
	return names
}
