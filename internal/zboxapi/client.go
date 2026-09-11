// Package zboxapi is a minimal client for the sandbox orchestrator REST API.
package zboxapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Client struct {
	BaseUrl string
	http    *http.Client
}

func New(baseUrl string) *Client {
	return &Client{
		BaseUrl: strings.TrimSuffix(baseUrl, "/"),
		http:    &http.Client{Timeout: 15 * time.Minute},
	}
}

type Sandbox struct {
	Id      string        `json:"id"`
	Name    string        `json:"name"`
	Image   string        `json:"image"`
	Status  string        `json:"status"`
	Ports   []SandboxPort `json:"ports"`
	Created time.Time     `json:"created"`
}

// A sandbox exposes zero or more ports and each one has its own URL, so there is no single sandbox URL to show.
type SandboxPort struct {
	Port int    `json:"port"`
	Url  string `json:"url"`
}

// PortNumbers is the set as the API takes it back in, ports are replaced whole rather than opened one by one.
func (s Sandbox) PortNumbers() []int {
	out := make([]int, 0, len(s.Ports))
	for _, p := range s.Ports {
		out = append(out, p.Port)
	}
	return out
}

type Ping struct {
	Time       time.Time `json:"time"`
	ImageReady bool      `json:"imageReady"`
}

type CreateRequest struct {
	Name  *string `json:"name"`
	Image *string `json:"image"`
	Ports []int   `json:"ports"`
}

type ExecRequest struct {
	Command        string  `json:"command"`
	WorkingDir     *string `json:"workingDir"`
	TimeoutSeconds *int    `json:"timeoutSeconds"`
}

type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

type ApiError struct {
	Status  int
	Code    string
	Message string
	// Details carries the per field complaints the API puts in the error meta, without them a validation failure reads as a bare "Invalid user input".
	Details []string
}

func (e ApiError) Error() string {
	out := fmt.Sprintf("%s (%d %s)", e.Message, e.Status, e.Code)
	if len(e.Details) > 0 {
		out += ": " + strings.Join(e.Details, ", ")
	}
	return out
}

func (c *Client) Ping(ctx context.Context) (Ping, error) {
	var out Ping
	return out, c.do(ctx, http.MethodGet, "/api/rest/ping", nil, &out)
}

func (c *Client) Create(ctx context.Context, req CreateRequest) (Sandbox, error) {
	var out Sandbox
	// The API has no nullable int array, so the port list is required and a nil slice would go out as null and come back as "field is required".
	if req.Ports == nil {
		req.Ports = []int{}
	}
	return out, c.do(ctx, http.MethodPost, "/api/rest/sandbox", req, &out)
}

func (c *Client) List(ctx context.Context) ([]Sandbox, error) {
	var out struct {
		Items []Sandbox `json:"items"`
	}
	return out.Items, c.do(ctx, http.MethodGet, "/api/rest/sandbox-list", nil, &out)
}

func (c *Client) Get(ctx context.Context, id string) (Sandbox, error) {
	var out Sandbox
	return out, c.do(ctx, http.MethodGet, "/api/rest/sandbox/"+id, nil, &out)
}

// SetPorts replaces the open set of a sandbox, which is how a port is opened after the sandbox already exists.
func (c *Client) SetPorts(ctx context.Context, id string, ports []int) (Sandbox, error) {
	var out Sandbox
	req := struct {
		Ports []int `json:"ports"`
	}{Ports: ports}
	return out, c.do(ctx, http.MethodPut, "/api/rest/sandbox/"+id+"/ports", req, &out)
}

func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/rest/sandbox/"+id, nil, nil)
}

func (c *Client) Exec(ctx context.Context, id string, req ExecRequest) (ExecResult, error) {
	var out ExecResult
	return out, c.do(ctx, http.MethodPost, "/api/rest/sandbox/"+id+"/exec", req, &out)
}

// ShellUrl is the WebSocket endpoint of the interactive shell.
func (c *Client) ShellUrl(id string, cols, rows int) string {
	u := c.BaseUrl
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return fmt.Sprintf("%s/api/rest/sandbox/%s/shell?cols=%d&rows=%d", u, id, cols, rows)
}

// WaitRunning polls a freshly created sandbox until it leaves CREATING.
func (c *Client) WaitRunning(ctx context.Context, id string) (Sandbox, error) {
	for {
		s, err := c.Get(ctx, id)
		if err != nil {
			return s, err
		}
		if s.Status != "CREATING" {
			return s, nil
		}
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (c *Client) do(ctx context.Context, method, p string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseUrl+p, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Meta    []struct {
					Metadata map[string][]string `json:"metadata"`
				} `json:"meta"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Code != "" {
			var details []string
			for _, meta := range envelope.Error.Meta {
				for field, messages := range meta.Metadata {
					details = append(details, field+" "+strings.Join(messages, " "))
				}
			}
			sort.Strings(details)
			return ApiError{Status: resp.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message, Details: details}
		}
		return ApiError{Status: resp.StatusCode, Code: "unexpected", Message: strings.TrimSpace(string(data))}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("invalid response from the orchestrator: " + err.Error())
	}
	return nil
}
