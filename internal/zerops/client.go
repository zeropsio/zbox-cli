// Package zerops wraps the public Zerops SDK for the handful of calls the CLI makes: login check, import, processes, service envs.
package zerops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	sdk    sdk.Handler
	apiUrl string
	token  string
	http   *http.Client
}

func New(apiUrl, token string) *Client {
	hc := &http.Client{Timeout: 60 * time.Second}
	h := sdk.New(sdkBase.DefaultConfig(sdkBase.WithCustomEndpoint(apiUrl)), hc)
	return &Client{sdk: sdk.AuthorizeSdk(h, token), apiUrl: strings.TrimSuffix(apiUrl, "/"), token: token, http: hc}
}

// Error is an answer of the public API to a call made outside the SDK, the `{"error": {code, message}}` envelope decoded.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Code)
	}
	return e.Message
}

// IsCode matches an error the public API named, from the SDK or from a call made outside it.
func IsCode(err error, code string) bool {
	var e Error
	if errors.As(err, &e) {
		return e.Code == code
	}
	return apiError.HasErrorCode(err, errorCode.ErrorCode(code))
}

// Access is the zproxy access of a workspace, the answer of POST /service-stack/{id}/ui-access: the orchestrator API is reached at UrlApi
// (`https://proxy.<domain>/service-api`) with Token as the bearer, which zproxy swaps for the workspace key; UrlUi is the browser form of the same access, the console.
// The token lives an hour past its last use. Minting needs BasicUser or above on the stack.
type Access struct {
	Token      string
	Expiration string
	UrlUi      string
	UrlApi     string
}

// UiAccess mints a zproxy access for the workspace service. The route is not in the released SDK yet, so it is posted by hand. A `urlApi` missing from the answer (a public API from before 2026-10-13) is derived from `urlUi`, the proxy host with the API prefix.
func (c *Client) UiAccess(ctx context.Context, serviceId string) (Access, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiUrl+"/api/rest/public/service-stack/"+serviceId+"/ui-access", nil)
	if err != nil {
		return Access{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Access{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Access{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &env)
		if env.Error.Message == "" {
			env.Error.Message = resp.Status
		}
		return Access{}, Error{Status: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message}
	}
	var out struct {
		AccessToken string `json:"accessToken"`
		Expiration  string `json:"expiration"`
		UrlUi       string `json:"urlUi"`
		UrlApi      string `json:"urlApi"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Access{}, fmt.Errorf("decode the ui-access answer: %w", err)
	}
	if out.AccessToken == "" {
		return Access{}, errors.New("the ui-access answer carries no access token")
	}
	a := Access{Token: out.AccessToken, Expiration: out.Expiration, UrlUi: out.UrlUi, UrlApi: strings.TrimSuffix(out.UrlApi, "/")}
	if a.UrlApi == "" {
		u, err := url.Parse(out.UrlUi)
		if err != nil || u.Host == "" {
			return Access{}, fmt.Errorf("the ui-access answer carries no usable URL: %q", out.UrlUi)
		}
		a.UrlApi = u.Scheme + "://" + u.Host + ProxyApiPrefix
	}
	return a, nil
}

// ProxyApiPrefix is where zproxy serves the header-authenticated form of a service's web UI and API, see Access.
const ProxyApiPrefix = "/service-api"

// IsNotFound recognises the answer the public API gives for a project or service that is gone, which for the CLI means a workspace deleted outside of it.
// The match is on the error code because the API answers these with HTTP 400 today, even though the ACL middleware that raises them was meant to answer 404. The status is checked too, so a fix on the API side keeps working.
func IsNotFound(err error) bool {
	var e Error
	if errors.As(err, &e) {
		return e.Code == string(errorCode.ProjectNotFound) || e.Code == string(errorCode.ServiceStackNotFound) || e.Status == http.StatusNotFound
	}
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

// RecipeSource marks the imported service as a zbox workspace. It rides in the request body, not the YAML, and the public API returns it on every service stack, which is how the Zerops GUI lists workspaces (`zeropsSetup` is not returned).
const RecipeSource = "zeropsio/zbox"

// WorkspaceStack is a workspace service of the account, a Box Workspace service (zbox@1).
type WorkspaceStack struct {
	ServiceId   string
	Hostname    string
	ProjectId   string
	ProjectName string
	Status      string
}

// workspaceType is the service stack type id of the Box Workspace service (`zbox`, `sandbox_workspace` until 2026-10-19), zbox@1 is only its import alias. The docker services the v0 workspaces were imported as are not listed any more, the orchestrator has no docker substrate since 2026-09-21.
const workspaceType = "zbox"

// Workspaces lists the workspace services of the organisation. The `recipeSource` marker is sent on import but the search index does not carry it, so it is not filtered on.
func (c *Client) Workspaces(ctx context.Context, clientId string) ([]WorkspaceStack, error) {
	var stacks []WorkspaceStack
	resp, err := c.sdk.PostServiceStackSearch(ctx, body.EsFilter{Search: body.EsFilterSearch{
		{Name: types.NewString("clientId"), Operator: types.NewString("eq"), Value: types.NewString(clientId)},
		{Name: types.NewString("serviceStackTypeId"), Operator: types.NewString("eq"), Value: types.NewString(workspaceType)},
	}})
	if err != nil {
		return nil, err
	}
	out, err := resp.Output()
	if err != nil {
		return nil, err
	}
	for _, item := range out.Items {
		stacks = append(stacks, WorkspaceStack{
			ServiceId:   string(item.Id),
			Hostname:    item.Name.Native(),
			ProjectId:   string(item.ProjectId),
			ProjectName: item.Project.Name.Native(),
			Status:      item.Status.Native(),
		})
	}
	return stacks, nil
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
	resp, err := c.sdk.PostClientProjectImport(ctx, path.ClientId{Id: cid}, body.ProjectImport{Yaml: types.NewText(yaml), RecipeSource: types.NewTextNull(RecipeSource)})
	if err != nil {
		return ImportResult{}, err
	}
	out, err := resp.Output()
	if err != nil {
		return ImportResult{}, err
	}
	return importResult(out), nil
}

// ImportServiceStack adds one service to an existing project, which is how a workspace is created next to whatever else the project already runs.
func (c *Client) ImportServiceStack(ctx context.Context, projectId, yaml string) (ImportResult, error) {
	pid, err := uuid.NewProjectIdFromString(projectId)
	if err != nil {
		return ImportResult{}, err
	}
	resp, err := c.sdk.PostProjectServiceStackImport(ctx, path.ProjectId{Id: pid}, body.ServiceStackImport{Yaml: types.NewText(yaml), RecipeSource: types.NewTextNull(RecipeSource)})
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

// DeleteServiceStack deletes one service and answers the id of the Zerops process doing it. The project around it stays.
func (c *Client) DeleteServiceStack(ctx context.Context, serviceId string) (string, error) {
	sid, err := uuid.NewServiceStackIdFromString(serviceId)
	if err != nil {
		return "", err
	}
	resp, err := c.sdk.DeleteServiceStack(ctx, path.ServiceStackId{Id: sid})
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
