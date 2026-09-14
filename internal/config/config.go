// Package config persists the CLI state: the Zerops token and API URL, the Zerops ids of the workspace created by `zbox setup`, and the workspace API URL and key every orchestrator call needs.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	DefaultApiUrl    = "https://api.app-futon.zerops.dev"
	DefaultGuiUrl    = "https://gui-3.app-futon.zerops.dev"
	DefaultBinaryUrl = "https://grg.zerops.dev/57fhv-s3/zbox"
	DefaultImageUrl  = "https://grg.zerops.dev/57fhv-s3/zbox-base.tar.gz"

	EnvConfigPath   = "ZBOX_CONFIG"
	EnvToken        = "ZBOX_TOKEN"
	EnvWorkspaceUrl = "ZBOX_WORKSPACE_URL"
	EnvWorkspaceKey = "ZBOX_API_KEY"
)

type Config struct {
	ApiUrl      string `json:"apiUrl"`
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

// EffectiveGuiUrl is where the browser login hand-off sends the user. It is stored rather than only a flag, so an install pointed at a locally running GUI stays pointed at it.
func (c Config) EffectiveGuiUrl() string {
	if c.GuiUrl != "" {
		return c.GuiUrl
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

// ForgetWorkspace drops the assignment, so the next `zbox setup` starts from scratch.
func (c *Config) ForgetWorkspace() error {
	c.ProjectId, c.ProjectName, c.ServiceId, c.WorkspaceUrl, c.ApiKey = "", "", "", "", ""
	return c.Save()
}
