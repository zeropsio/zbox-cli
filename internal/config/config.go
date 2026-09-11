// Package config persists the CLI state: the Zerops token and API URL plus the ids of the sandbox pool created by `zbox setup`.
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

	EnvConfigPath = "ZBOX_CONFIG"
	EnvToken      = "ZBOX_TOKEN"
)

type Config struct {
	ApiUrl      string `json:"apiUrl"`
	GuiUrl      string `json:"guiUrl,omitempty"`
	Token       string `json:"token,omitempty"`
	ClientId    string `json:"clientId,omitempty"`
	ProjectId   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	ServiceId   string `json:"serviceId,omitempty"`
	ZboxUrl     string `json:"zboxUrl,omitempty"`
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

// One Zerops service is one sandbox pool, so the pool is identified by the project and service it runs in.
func (c Config) HasSandboxPool() bool {
	return c.ProjectId != "" && c.ServiceId != "" && c.ZboxUrl != ""
}

// ForgetSandboxPool drops the assignment of a pool whose project is gone, so the next `zbox setup` starts from scratch.
func (c *Config) ForgetSandboxPool() error {
	c.ProjectId, c.ProjectName, c.ServiceId, c.ZboxUrl = "", "", "", ""
	return c.Save()
}
