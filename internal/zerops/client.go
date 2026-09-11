// Package zerops wraps the public Zerops SDK for the handful of calls the CLI makes.
package zerops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zeropsio/zerops-go/apiError"
	"github.com/zeropsio/zerops-go/dto/input/body"
	"github.com/zeropsio/zerops-go/dto/input/path"
	"github.com/zeropsio/zerops-go/dto/output"
	"github.com/zeropsio/zerops-go/errorCode"
	"github.com/zeropsio/zerops-go/sdk"
	"github.com/zeropsio/zerops-go/sdkBase"
	"github.com/zeropsio/zerops-go/types"
	"github.com/zeropsio/zerops-go/types/enum"
	"github.com/zeropsio/zerops-go/types/uuid"
)

type Client struct {
	sdk sdk.Handler
}

func New(apiUrl, token string) *Client {
	h := sdk.New(sdkBase.DefaultConfig(sdkBase.WithCustomEndpoint(apiUrl)), &http.Client{Timeout: 60 * time.Second})
	return &Client{sdk: sdk.AuthorizeSdk(h, token)}
}

// IsNotFound recognises the answer the public API gives for a project or service that is gone, which for the CLI means a pool deleted outside of it.
// The match is on the error code because the API answers these with HTTP 400 today, even though the ACL middleware that raises them was meant to answer 404. The status is checked too, so a fix on the API side keeps working.
func IsNotFound(err error) bool {
	return apiError.HasErrorCode(err, errorCode.ProjectNotFound) ||
		apiError.HasErrorCode(err, errorCode.ServiceStackNotFound) ||
		apiError.HasHTTPCode(err, http.StatusNotFound)
}

type User struct {
	Email    string
	FullName string
	ClientId string
	Account  string
}

// UserInfo validates the token and picks the first organisation the user may create projects in.
func (c *Client) UserInfo(ctx context.Context) (User, error) {
	resp, err := c.sdk.GetUserInfo(ctx)
	if err != nil {
		return User{}, err
	}
	info, err := resp.Output()
	if err != nil {
		return User{}, err
	}
	u := User{Email: info.Email.Native(), FullName: info.FullName.Native()}
	for _, cu := range info.ClientUserList {
		if cu.CanCreateProjects.Native() {
			u.ClientId = cu.ClientId.Native()
			u.Account = cu.Client.AccountName.Native()
			break
		}
	}
	if u.ClientId == "" {
		return u, errors.New("the account is not allowed to create projects in any organisation")
	}
	return u, nil
}

type ImportResult struct {
	ProjectId   string
	ProjectName string
	ServiceId   string
	Processes   []string
}

func (c *Client) ImportProject(ctx context.Context, clientId, yaml string) (ImportResult, error) {
	cid, err := uuid.NewClientIdFromString(clientId)
	if err != nil {
		return ImportResult{}, err
	}
	resp, err := c.sdk.PostClientProjectImport(ctx, path.ClientId{Id: cid}, body.ProjectImport{Yaml: types.NewText(yaml)})
	if err != nil {
		return ImportResult{}, err
	}
	out, err := resp.Output()
	if err != nil {
		return ImportResult{}, err
	}
	return importResult(out), nil
}

// ImportServiceStack adds one service to an existing project, which is how a pool is created next to whatever else the project already runs.
func (c *Client) ImportServiceStack(ctx context.Context, projectId, yaml string) (ImportResult, error) {
	pid, err := uuid.NewProjectIdFromString(projectId)
	if err != nil {
		return ImportResult{}, err
	}
	resp, err := c.sdk.PostProjectServiceStackImport(ctx, path.ProjectId{Id: pid}, body.ServiceStackImport{Yaml: types.NewText(yaml)})
	if err != nil {
		return ImportResult{}, err
	}
	out, err := resp.Output()
	if err != nil {
		return ImportResult{}, err
	}
	return importResult(out), nil
}

func importResult(out output.ProjectImport) ImportResult {
	res := ImportResult{ProjectId: out.ProjectId.Native(), ProjectName: out.ProjectName.Native()}
	for _, s := range out.ServiceStacks {
		res.ServiceId = s.Id.Native()
		for _, p := range s.Processes {
			res.Processes = append(res.Processes, p.Id.Native())
		}
	}
	return res
}

func (c *Client) DeleteProject(ctx context.Context, projectId string) (string, error) {
	pid, err := uuid.NewProjectIdFromString(projectId)
	if err != nil {
		return "", err
	}
	resp, err := c.sdk.DeleteProject(ctx, path.ProjectId{Id: pid})
	if err != nil {
		return "", err
	}
	out, err := resp.Output()
	if err != nil {
		return "", err
	}
	return out.Id.Native(), nil
}

func (c *Client) Project(ctx context.Context, projectId string) (output.Project, error) {
	pid, err := uuid.NewProjectIdFromString(projectId)
	if err != nil {
		return output.Project{}, err
	}
	resp, err := c.sdk.GetProject(ctx, path.ProjectId{Id: pid})
	if err != nil {
		return output.Project{}, err
	}
	return resp.Output()
}

func (c *Client) ServiceStack(ctx context.Context, serviceId string) (output.ServiceStack, error) {
	sid, err := uuid.NewServiceStackIdFromString(serviceId)
	if err != nil {
		return output.ServiceStack{}, err
	}
	resp, err := c.sdk.GetServiceStack(ctx, path.ServiceStackId{Id: sid})
	if err != nil {
		return output.ServiceStack{}, err
	}
	return resp.Output()
}

// ServiceEnv returns the value of one generated service variable, for example zeropsSubdomain.
func (c *Client) ServiceEnv(ctx context.Context, serviceId, key string) (string, bool, error) {
	sid, err := uuid.NewServiceStackIdFromString(serviceId)
	if err != nil {
		return "", false, err
	}
	resp, err := c.sdk.GetServiceStackEnv(ctx, path.ServiceStackId{Id: sid})
	if err != nil {
		return "", false, err
	}
	out, err := resp.Output()
	if err != nil {
		return "", false, err
	}
	for _, e := range out.Items {
		if e.Key.Native() == key {
			return e.Content.Native(), true, nil
		}
	}
	return "", false, nil
}

type ProcessState struct {
	Id     string
	Action string
	Status string
	Error  string
}

func (c *Client) Process(ctx context.Context, processId string) (ProcessState, error) {
	pid, err := uuid.NewProcessIdFromString(processId)
	if err != nil {
		return ProcessState{}, err
	}
	resp, err := c.sdk.GetProcess(ctx, path.ProcessId{Id: pid})
	if err != nil {
		return ProcessState{}, err
	}
	p, err := resp.Output()
	if err != nil {
		return ProcessState{}, err
	}
	state := ProcessState{Id: p.Id.Native(), Action: p.ActionName.Native(), Status: p.Status.Native()}
	if p.Error != nil {
		state.Error = strings.TrimSpace(p.Error.Code.Native() + " " + p.Error.Message.Native())
	}
	return state, nil
}

func IsTerminal(status string) bool {
	switch enum.ProcessStatusEnum(status) {
	case enum.ProcessStatusEnumFinished, enum.ProcessStatusEnumFailed, enum.ProcessStatusEnumCanceled:
		return true
	}
	return false
}

// WaitProcesses polls the given processes every two seconds and reports every status change through onChange.
// It returns an error when any process ends in a state other than FINISHED.
func (c *Client) WaitProcesses(ctx context.Context, ids []string, onChange func(ProcessState)) error {
	last := map[string]string{}
	pending := append([]string(nil), ids...)
	var failed []ProcessState
	for len(pending) > 0 {
		var still []string
		for _, id := range pending {
			p, err := c.Process(ctx, id)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Transient API errors are tolerated, the next tick retries.
				still = append(still, id)
				continue
			}
			if last[id] != p.Status {
				last[id] = p.Status
				onChange(p)
			}
			if !IsTerminal(p.Status) {
				still = append(still, id)
				continue
			}
			if enum.ProcessStatusEnum(p.Status) != enum.ProcessStatusEnumFinished {
				failed = append(failed, p)
			}
		}
		pending = still
		if len(pending) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if len(failed) > 0 {
		var parts []string
		for _, p := range failed {
			parts = append(parts, fmt.Sprintf("%s %s %s", p.Action, p.Status, p.Error))
		}
		return errors.New("process failed: " + strings.Join(parts, "; "))
	}
	return nil
}
