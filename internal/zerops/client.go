// Package zerops wraps the public Zerops SDK for the handful of calls the CLI makes: login check, import, start, stop, restart, reload and size of a service, processes, service envs.
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

	"golang.org/x/sync/errgroup"

	"github.com/zeropsio/zerops-go/apiError"
	"github.com/zeropsio/zerops-go/dto/input/body"
	"github.com/zeropsio/zerops-go/dto/input/path"
	"github.com/zeropsio/zerops-go/dto/input/query"
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
	// pollInterval is the pause between two rounds of WaitProcesses.
	pollInterval time.Duration
}

func New(apiUrl, token string) *Client {
	hc := &http.Client{Timeout: 60 * time.Second}
	h := sdk.New(sdkBase.DefaultConfig(sdkBase.WithCustomEndpoint(apiUrl)), hc)
	return &Client{sdk: sdk.AuthorizeSdk(h, token), apiUrl: strings.TrimSuffix(apiUrl, "/"), token: token, http: hc, pollInterval: 2 * time.Second}
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

// AsError reads the answer of the public API out of err, from the SDK or from a call made outside it.
func AsError(err error) (Error, bool) {
	var e Error
	if errors.As(err, &e) {
		return e, true
	}
	var se apiError.Error
	if errors.As(err, &se) {
		return Error{Status: se.HttpStatusCode, Code: se.ErrorCode, Message: se.Message}, true
	}
	return Error{}, false
}

// IsCode matches an error the public API named.
func IsCode(err error, code string) bool {
	e, ok := AsError(err)
	return ok && e.Code == code
}

// Access is the zproxy access of a workspace, the answer of POST /service-stack/{id}/ui-access: the orchestrator API is reached at UrlApi
// (`https://proxy.<domain>/service-api`) with Token as the bearer, which zproxy swaps for the workspace key.
// The token lives an hour past its last use. Minting needs BasicUser or above on the stack.
type Access struct {
	Token      string
	Expiration string
	UrlApi     string
}

// UiAccess mints a zproxy access for the workspace service. The route is not in the released SDK yet, so it is posted by hand. A `urlApi` missing from the answer (an older public API) is derived from `urlUi`, the proxy host with the API prefix.
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
	a := Access{Token: out.AccessToken, Expiration: out.Expiration, UrlApi: strings.TrimSuffix(out.UrlApi, "/")}
	if a.UrlApi == "" {
		u, err := url.Parse(out.UrlUi)
		if err != nil || u.Host == "" {
			return Access{}, errors.New("the ui-access answer carries no usable URL")
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
	e, ok := AsError(err)
	return ok && (e.Code == string(errorCode.ProjectNotFound) || e.Code == string(errorCode.ServiceStackNotFound) || e.Status == http.StatusNotFound)
}

// deniedOrGone says whether the API answered that the token cannot reach what was asked for, or that it is gone.
func deniedOrGone(err error) bool {
	e, ok := AsError(err)
	return ok && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden || IsCode(err, string(errorCode.ProcessNotFound)) || IsNotFound(err))
}

// listSkipped says whether one organisation or project is left out of `Workspaces` rather than failing the list.
// The user has no role there or it is gone, so the list is still complete from the user's view.
func listSkipped(err error) bool {
	e, ok := AsError(err)
	return ok && (e.Status == http.StatusForbidden || e.Code == string(errorCode.InsufficientPermissions) || e.Code == string(errorCode.ClientNotFound) || IsNotFound(err))
}

type User struct {
	Email    string
	FullName string
	// ClientId and Account are the first organisation the user may create projects in, empty when there is none.
	ClientId string
	Account  string
	// Organizations are the active memberships, in the order the API lists them.
	Organizations []Organization
}

// Organization is one Zerops organisation (a client) the user is an active member of.
type Organization struct {
	Id                string
	Name              string
	CanCreateProjects bool
}

// UserInfo validates the token and reads the organisations of the user.
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
		// A pending invitation reaches nothing in the organisation yet.
		if cu.Status != enum.ClientUserStatusEnumActive {
			continue
		}
		org := Organization{Id: cu.ClientId.Native(), Name: cu.Client.AccountName.Native(), CanCreateProjects: cu.CanCreateProjects.Native()}
		u.Organizations = append(u.Organizations, org)
		if org.CanCreateProjects && u.ClientId == "" {
			u.ClientId, u.Account = org.Id, org.Name
		}
	}
	return u, nil
}

// ErrCreateProjects is the refusal of ProjectOrganization when the role does not allow a new project.
var ErrCreateProjects = errors.New("the account is not allowed to create projects")

// ProjectOrganization is the organisation a new project is created in: the one ref names by id or name, the first that allows it when ref is empty.
func (u User) ProjectOrganization(ref string) (Organization, error) {
	if ref == "" {
		if u.ClientId == "" {
			return Organization{}, fmt.Errorf("%w in any organisation", ErrCreateProjects)
		}
		return Organization{Id: u.ClientId, Name: u.Account, CanCreateProjects: true}, nil
	}
	var hits []Organization
	for _, o := range u.Organizations {
		if o.Id == ref || strings.EqualFold(o.Name, ref) {
			hits = append(hits, o)
		}
	}
	switch len(hits) {
	case 0:
		return Organization{}, fmt.Errorf("no organisation of the account matches %q, it is a member of: %s", ref, u.organizationNames())
	case 1:
		if !hits[0].CanCreateProjects {
			return Organization{}, fmt.Errorf("%w in organisation %s (%s)", ErrCreateProjects, hits[0].Name, hits[0].Id)
		}
		return hits[0], nil
	}
	ids := make([]string, len(hits))
	for i, o := range hits {
		ids[i] = o.Id
	}
	return Organization{}, fmt.Errorf("%q names several organisations, give the id: %s", ref, strings.Join(ids, ", "))
}

func (u User) organizationNames() string {
	if len(u.Organizations) == 0 {
		return "none"
	}
	names := make([]string, len(u.Organizations))
	for i, o := range u.Organizations {
		names[i] = fmt.Sprintf("%s (%s)", o.Name, o.Id)
	}
	return strings.Join(names, ", ")
}

// RecipeSource marks the imported service as a zbox workspace. It rides in the request body, not the YAML, and the public API returns it on every service stack, which is how the Zerops GUI lists workspaces (`zeropsSetup` is not returned).
const RecipeSource = "zeropsio/zbox"

// WorkspaceStack is a workspace service of the account, a Box Workspace service (zbox@1).
type WorkspaceStack struct {
	ServiceId        string `json:"serviceId"`
	Hostname         string `json:"hostname"`
	ProjectId        string `json:"projectId"`
	ProjectName      string `json:"projectName"`
	OrganizationId   string `json:"organizationId"`
	OrganizationName string `json:"organizationName"`
	Status           string `json:"status"`
}

// workspaceType is the service stack type id of the Box Workspace service, zbox@1 is only its import alias.
const workspaceType = "zbox"

const (
	// listPageSize is the page one list call asks for. The API clamps a larger one to 1000 and echoes the limit it used.
	listPageSize = 100
	// listConcurrency bounds the list calls in flight while `Workspaces` walks the projects.
	listConcurrency = 8
)

// project is one project of an organisation, as `Workspaces` walks them.
type project struct {
	org  Organization
	id   string
	name string
}

// Workspaces lists the workspace services of the organisations: every project of each, then the services of every project, kept when their type is the Box Workspace.
// The `recipeSource` marker is sent on import but the lists do not filter on it, the type does.
// An organisation that answers forbidden or not-found is left out and returned in skipped, a project that does is left out silently.
// The project list returns every project of the organisation, also those that the user has no role in.
func (c *Client) Workspaces(ctx context.Context, orgs []Organization) (stacks []WorkspaceStack, skipped []Organization, err error) {
	projectsOf := make([][]project, len(orgs))
	skippedOrg := make([]bool, len(orgs))
	if err := forEach(ctx, len(orgs), func(ctx context.Context, i int) error {
		var err error
		projectsOf[i], err = c.projects(ctx, orgs[i])
		switch {
		case err == nil:
		case listSkipped(err):
			projectsOf[i], skippedOrg[i] = nil, true
		default:
			return fmt.Errorf("list the projects of organisation %s: %w", orgs[i].Name, err)
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	var projects []project
	for i, ps := range projectsOf {
		if skippedOrg[i] {
			skipped = append(skipped, orgs[i])
		}
		projects = append(projects, ps...)
	}
	stacksOf := make([][]WorkspaceStack, len(projects))
	if err := forEach(ctx, len(projects), func(ctx context.Context, i int) error {
		var err error
		stacksOf[i], err = c.projectWorkspaces(ctx, projects[i])
		switch {
		case err == nil:
		case listSkipped(err):
			stacksOf[i] = nil
		default:
			return fmt.Errorf("list the services of project %s in organisation %s: %w", projects[i].name, projects[i].org.Name, err)
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	stacks = []WorkspaceStack{}
	for _, ss := range stacksOf {
		stacks = append(stacks, ss...)
	}
	return stacks, skipped, nil
}

// projects pages through `GET /client/{id}/project`.
func (c *Client) projects(ctx context.Context, org Organization) ([]project, error) {
	cid, err := uuid.NewClientIdFromString(org.Id)
	if err != nil {
		return nil, err
	}
	var out []project
	err = pages(func(offset int) (int, int, error) {
		resp, err := c.sdk.GetClientProject(ctx, path.ClientId{Id: cid}, query.ListClientProjects{Limit: types.NewIntNull(listPageSize), Offset: types.NewIntNull(offset)})
		if err != nil {
			return 0, 0, err
		}
		list, err := resp.Output()
		if err != nil {
			return 0, 0, err
		}
		for _, p := range list.List {
			out = append(out, project{org: org, id: p.Id.Native(), name: p.Name.Native()})
		}
		return list.Limit.Native(), list.Total.Native(), nil
	})
	return out, err
}

// projectWorkspaces pages through `GET /project/{id}/service-stack` and keeps the Box Workspace services.
func (c *Client) projectWorkspaces(ctx context.Context, p project) ([]WorkspaceStack, error) {
	pid, err := uuid.NewProjectIdFromString(p.id)
	if err != nil {
		return nil, err
	}
	var out []WorkspaceStack
	err = pages(func(offset int) (int, int, error) {
		resp, err := c.sdk.GetProjectServiceStack(ctx, path.ProjectId{Id: pid}, query.ListProjectServiceStacks{Limit: types.NewIntNull(listPageSize), Offset: types.NewIntNull(offset)})
		if err != nil {
			return 0, 0, err
		}
		list, err := resp.Output()
		if err != nil {
			return 0, 0, err
		}
		for _, st := range list.List {
			if st.ServiceStackTypeId.Native() != workspaceType {
				continue
			}
			out = append(out, WorkspaceStack{
				ServiceId:        st.Id.Native(),
				Hostname:         st.Name.Native(),
				ProjectId:        p.id,
				ProjectName:      p.name,
				OrganizationId:   p.org.Id,
				OrganizationName: p.org.Name,
				Status:           st.Status.Native(),
			})
		}
		return list.Limit.Native(), list.Total.Native(), nil
	})
	return out, err
}

// pages calls fetch for every page until the offset reaches the total. The offset moves by the limit that the API echoes, since the API clamps the limit asked for.
func pages(fetch func(offset int) (limit, total int, err error)) error {
	for offset := 0; ; {
		limit, total, err := fetch(offset)
		if err != nil {
			return err
		}
		if limit <= 0 {
			limit = listPageSize
		}
		offset += limit
		if offset >= total {
			return nil
		}
	}
}

// forEach runs fn for 0..n-1 with at most listConcurrency calls at once. The first error cancels the others and is returned.
func forEach(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(listConcurrency)
	for i := range n {
		g.Go(func() error { return fn(ctx, i) })
	}
	return g.Wait()
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

// StartServiceStack answers the Zerops process that starts the service, `PUT /service-stack/{id}/start`.
func (c *Client) StartServiceStack(ctx context.Context, serviceId string) (output.Process, error) {
	return serviceAction(ctx, serviceId, c.sdk.PutServiceStackStart)
}

// StopServiceStack answers the Zerops process that stops the service, `PUT /service-stack/{id}/stop`.
func (c *Client) StopServiceStack(ctx context.Context, serviceId string) (output.Process, error) {
	return serviceAction(ctx, serviceId, c.sdk.PutServiceStackStop)
}

// RestartServiceStack answers the Zerops process that restarts the service, `PUT /service-stack/{id}/restart`.
func (c *Client) RestartServiceStack(ctx context.Context, serviceId string) (output.Process, error) {
	return serviceAction(ctx, serviceId, c.sdk.PutServiceStackRestart)
}

// ReloadServiceStack answers the Zerops process that reloads the service, `PUT /service-stack/{id}/reload`.
func (c *Client) ReloadServiceStack(ctx context.Context, serviceId string) (output.Process, error) {
	return serviceAction(ctx, serviceId, c.sdk.PutServiceStackReload)
}

// processAnswer is an SDK response that carries a process.
type processAnswer interface {
	Output() (output.Process, error)
}

func serviceAction[R processAnswer](ctx context.Context, serviceId string, call func(context.Context, path.ServiceStackId) (R, error)) (output.Process, error) {
	sid, err := uuid.NewServiceStackIdFromString(serviceId)
	if err != nil {
		return output.Process{}, err
	}
	resp, err := call(ctx, path.ServiceStackId{Id: sid})
	if err != nil {
		return output.Process{}, err
	}
	return resp.Output()
}

// Autoscale sends `PUT /service-stack/{id}/autoscaling`. The answer carries no process when the service has nothing deployed to apply it to.
func (c *Client) Autoscale(ctx context.Context, serviceId string, in body.Autoscaling) (*output.Process, error) {
	sid, err := uuid.NewServiceStackIdFromString(serviceId)
	if err != nil {
		return nil, err
	}
	resp, err := c.sdk.PutServiceStackAutoscaling(ctx, path.ServiceStackId{Id: sid}, in)
	if err != nil {
		return nil, err
	}
	out, err := resp.Output()
	if err != nil {
		return nil, err
	}
	return out.Process, nil
}

// Size is the vertical autoscaling of a workspace VM, read from the maximum since a workspace pins min equal to max. Ram and Disk are GiB.
// CpuMode is DEDICATED or SHARED, empty when the service names none.
type Size struct {
	CpuMode string  `json:"cpuMode,omitempty"`
	Cpu     int     `json:"cpu"`
	Ram     float64 `json:"ramGiB"`
	Disk    float64 `json:"diskGiB"`
}

// Known says whether the service names any resource at all.
func (s Size) Known() bool {
	return s.Cpu > 0 || s.Ram > 0 || s.Disk > 0
}

// StackSize is the size the platform applies to the service, its currentAutoscaling. The custom autoscaling stands in when the service has no current one.
func StackSize(stack output.ServiceStack) Size {
	va := vertical(stack.CurrentAutoscaling)
	if va == nil {
		va = vertical(stack.CustomAutoscaling)
	}
	if va == nil {
		return Size{}
	}
	var s Size
	if va.CpuMode != nil {
		s.CpuMode = string(*va.CpuMode)
	}
	if r := va.MaxResource; r != nil {
		s.Cpu = intOf(r.CpuCoreCount)
		s.Ram = floatOf(r.MemoryGBytes)
		s.Disk = floatOf(r.DiskGBytes)
	}
	return s
}

func vertical(ca *output.CustomAutoscaling) *output.VerticalAutoscalingNullable {
	if ca == nil {
		return nil
	}
	return ca.VerticalAutoscaling
}

// Resize is what `zbox workspace resize` changes. A zero resource and an empty CpuMode keep what the service has.
type Resize struct {
	CpuMode string
	Cpu     int
	Ram     int
	Disk    int
}

// Applied says whether the service already has the cpu mode and every resource r names, each pinned as min and max.
// The stored custom autoscaling counts too, since it keeps a size above the account's limit that the current one is trimmed from.
func Applied(stack output.ServiceStack, r Resize) bool {
	return pinned(vertical(stack.CurrentAutoscaling), r) || pinned(vertical(stack.CustomAutoscaling), r)
}

func pinned(va *output.VerticalAutoscalingNullable, r Resize) bool {
	if va == nil || va.MinResource == nil || va.MaxResource == nil {
		return false
	}
	if r.CpuMode != "" && (va.CpuMode == nil || string(*va.CpuMode) != r.CpuMode) {
		return false
	}
	lo, hi := va.MinResource, va.MaxResource
	if r.Cpu > 0 && (intOf(lo.CpuCoreCount) != r.Cpu || intOf(hi.CpuCoreCount) != r.Cpu) {
		return false
	}
	if r.Ram > 0 && (floatOf(lo.MemoryGBytes) != float64(r.Ram) || floatOf(hi.MemoryGBytes) != float64(r.Ram)) {
		return false
	}
	if r.Disk > 0 && (floatOf(lo.DiskGBytes) != float64(r.Disk) || floatOf(hi.DiskGBytes) != float64(r.Disk)) {
		return false
	}
	return true
}

// DiskFloor is the disk a resize cannot go under: the larger of the current and the custom max disk.
// The platform compares a new disk with the custom one, which stays above the current one when the size is trimmed.
func DiskFloor(stack output.ServiceStack) float64 {
	floor := StackSize(stack).Disk
	if va := vertical(stack.CustomAutoscaling); va != nil && va.MaxResource != nil {
		floor = max(floor, floatOf(va.MaxResource.DiskGBytes))
	}
	return floor
}

func intOf(v types.IntNull) int {
	n, _ := v.Get()
	return n.Native()
}

func floatOf(v types.FloatNull) float64 {
	n, _ := v.Get()
	return n.Native()
}

// AutoscalingBody is the body of `PUT /service-stack/{id}/autoscaling` that applies r to the service.
// The endpoint replaces the stored custom autoscaling with the one in the body, so the body carries the stored one with only what r names changed.
// The autoscaling profile is sent back as it is, because the endpoint drops a profile that the body leaves out.
func AutoscalingBody(stack output.ServiceStack, r Resize) (body.Autoscaling, error) {
	var custom body.CustomAutoscaling
	if stack.CustomAutoscaling != nil {
		raw, err := json.Marshal(stack.CustomAutoscaling)
		if err != nil {
			return body.Autoscaling{}, err
		}
		if err := json.Unmarshal(raw, &custom); err != nil {
			return body.Autoscaling{}, fmt.Errorf("read the autoscaling of the service: %w", err)
		}
	}
	va := custom.VerticalAutoscaling
	if va == nil {
		va = &body.VerticalAutoscalingNullable{}
		custom.VerticalAutoscaling = va
	}
	if va.MinResource == nil {
		va.MinResource = &body.ScalingResourceNullable{}
	}
	if va.MaxResource == nil {
		va.MaxResource = &body.ScalingResourceNullable{}
	}
	if r.CpuMode != "" {
		mode := enum.VerticalAutoscalingCpuModeEnum(r.CpuMode)
		va.CpuMode = &mode
	}
	if r.Cpu > 0 {
		va.MinResource.CpuCoreCount = types.NewIntNull(r.Cpu)
		va.MaxResource.CpuCoreCount = types.NewIntNull(r.Cpu)
	}
	if r.Ram > 0 {
		va.MinResource.MemoryGBytes = types.NewFloatNull(float64(r.Ram))
		va.MaxResource.MemoryGBytes = types.NewFloatNull(float64(r.Ram))
	}
	if r.Disk > 0 {
		va.MinResource.DiskGBytes = types.NewFloatNull(float64(r.Disk))
		va.MaxResource.DiskGBytes = types.NewFloatNull(float64(r.Disk))
	}
	out := body.Autoscaling{CustomAutoscaling: &custom, AutoscalingProfileId: stack.AutoscalingProfileId}
	if overrides, set := stack.AutoscalingProfileOverrides.Get(); set && stack.AutoscalingProfileId.Filled() && len(overrides) > 0 {
		raw, err := json.Marshal(overrides)
		if err != nil {
			return body.Autoscaling{}, err
		}
		out.AutoscalingProfileOverrides = types.JsonRawMessage(raw)
	}
	return out, nil
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
	p, err := c.ProcessOutput(ctx, processId)
	if err != nil {
		return ProcessState{}, err
	}
	state := ProcessState{Id: p.Id.Native(), Action: p.ActionName.Native(), Status: p.Status.Native()}
	if p.Error != nil {
		state.Error = strings.TrimSpace(p.Error.Code.Native() + " " + p.Error.Message.Native())
	}
	return state, nil
}

// ProcessOutput is the process as `GET /process/{id}` answers it.
func (c *Client) ProcessOutput(ctx context.Context, processId string) (output.Process, error) {
	pid, err := uuid.NewProcessIdFromString(processId)
	if err != nil {
		return output.Process{}, err
	}
	resp, err := c.sdk.GetProcess(ctx, path.ProcessId{Id: pid})
	if err != nil {
		return output.Process{}, err
	}
	return resp.Output()
}

func IsTerminal(status string) bool {
	switch enum.ProcessStatusEnum(status) {
	case enum.ProcessStatusEnumFinished, enum.ProcessStatusEnumFailed, enum.ProcessStatusEnumCanceled:
		return true
	}
	return false
}

// maxPollFailures is how many reads of one process in a row may fail before WaitProcesses gives up.
const maxPollFailures = 5

// WaitProcesses polls the given processes and reports every status change through onChange.
// It returns an error when any process ends in a state other than FINISHED, when the API refuses a read or answers the process is gone,
// and when maxPollFailures reads of one process in a row fail.
func (c *Client) WaitProcesses(ctx context.Context, ids []string, onChange func(ProcessState)) error {
	last := map[string]string{}
	failures := map[string]int{}
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
				if deniedOrGone(err) {
					return fmt.Errorf("read the Zerops process %s: %w", id, err)
				}
				if failures[id]++; failures[id] >= maxPollFailures {
					return fmt.Errorf("read the Zerops process %s, %d attempts in a row failed: %w", id, failures[id], err)
				}
				still = append(still, id)
				continue
			}
			failures[id] = 0
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
		case <-time.After(c.pollInterval):
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
