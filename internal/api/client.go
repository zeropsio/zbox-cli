package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Workspace is the generated client with the workspace API key on every request and the responses decoded into the shared Base* types.
// Every method mirrors one route of the contract, nothing exists here that the spec does not have.
type Workspace struct {
	BaseUrl string
	apiKey  string
	gen     *ClientWithResponses
	http    *http.Client
}

// ApiKey is the workspace key the client sends as the bearer, printed by `zbox workspace key`. It never goes into a URL: the API, the WebSocket routes and the MEMBERS ports take it as an Authorization bearer only.
func (w *Workspace) ApiKey() string { return w.apiKey }

func New(baseUrl, apiKey string) *Workspace {
	baseUrl = strings.TrimSuffix(baseUrl, "/")
	c := &Workspace{BaseUrl: baseUrl, apiKey: apiKey, http: &http.Client{Timeout: 15 * time.Minute}}
	c.gen, _ = NewClientWithResponses(baseUrl, WithHTTPClient(c.http), WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		return nil
	}))
	return c
}

// Key mints the idempotency key of one capacity-consuming call. One per logical call, reused on a retry of that call, never shared between two.
func Key() string {
	return uuid.NewString()
}

type ApiError struct {
	Status  int
	Code    string
	Message string
	// Details carries the per field complaints of a validation failure, without them it reads as a bare "Invalid user input".
	Details []string
	// Meta is the same metadata by field, for callers that act on it, for example the operationId of an operationInFlight answer.
	Meta map[string][]string
}

// MetaValue is the first value the API put under a metadata field, empty when there is none.
func MetaValue(err error, field string) string {
	var e ApiError
	if errors.As(err, &e) && len(e.Meta[field]) > 0 {
		return e.Meta[field][0]
	}
	return ""
}

func (e ApiError) Error() string {
	out := e.Message
	if e.Code != "" {
		out += fmt.Sprintf(" (%s)", e.Code)
	}
	if len(e.Details) > 0 {
		out += ": " + strings.Join(e.Details, ", ")
	}
	return out
}

// IsCode matches an error the API named, for example sandboxNotFound.
func IsCode(err error, code string) bool {
	var e ApiError
	return errors.As(err, &e) && e.Code == code
}

func decodeError(resp *http.Response, body []byte) error {
	// The contract says a flat {code, message}, the shared HTTP server wraps it in {"error": {...}} with per field meta. Both are accepted.
	var flat struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Meta    []struct {
				Metadata map[string][]string `json:"metadata"`
			} `json:"meta"`
		} `json:"error"`
	}
	e := ApiError{Status: resp.StatusCode}
	if json.Unmarshal(body, &flat) == nil {
		e.Code, e.Message = flat.Code, flat.Message
		if flat.Error != nil {
			e.Code, e.Message = flat.Error.Code, flat.Error.Message
			for _, meta := range flat.Error.Meta {
				for field, messages := range meta.Metadata {
					e.Details = append(e.Details, field+" "+strings.Join(messages, " "))
					if e.Meta == nil {
						e.Meta = map[string][]string{}
					}
					e.Meta[field] = append(e.Meta[field], messages...)
				}
			}
			sort.Strings(e.Details)
		}
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(body))
		if e.Message == "" || len(e.Message) > 200 {
			e.Message = resp.Status
		}
	}
	if resp.StatusCode == http.StatusUnauthorized && e.Code == "" {
		e.Code = "unauthorized"
	}
	return e
}

// ok decodes a 200 body into T, any other answer becomes an ApiError.
func ok[T any](resp *http.Response, err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeError(resp, body)
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, errors.New("invalid response from the workspace: " + err.Error())
	}
	return &out, nil
}

func items[T any](resp *http.Response, err error) ([]T, error) {
	out, err := ok[struct {
		Items []T `json:"items"`
	}](resp, err)
	if err != nil {
		return nil, err
	}
	return out.Items, nil
}

func done(resp *http.Response, err error) error {
	_, err = ok[ResponseSuccess](resp, err)
	return err
}

// stream hands the body of a download route over unparsed.
func stream(resp *http.Response, err error) (io.ReadCloser, error) {
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, decodeError(resp, body)
	}
	return resp.Body, nil
}

func ptr[T any](v T) *T { return &v }

// Time parses a timestamp of the API. The contract types them as strings, an unparsable one comes back zero rather than failing the command.
func Time(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ---- workspace

func (c *Workspace) Ping(ctx context.Context) (*ResponseGetPing, error) {
	return ok[ResponseGetPing](c.gen.GetPing(ctx))
}

func (c *Workspace) Info(ctx context.Context) (*ResponseWorkspace, error) {
	return ok[ResponseWorkspace](c.gen.GetWorkspace(ctx))
}

func (c *Workspace) PutWorkspacePolicy(ctx context.Context, body RequestPutWorkspacePolicy) (*ResponseWorkspace, error) {
	return ok[ResponseWorkspace](c.gen.PutWorkspacePolicy(ctx, body))
}

// ---- base and template

// CreateBase answers the PULL_BASE operation, the pull runs on: a tarball takes longer than the edge holds a request.
func (c *Workspace) CreateBase(ctx context.Context, body RequestPostBase) (*BaseOperation, error) {
	return ok[BaseOperation](c.gen.PostBase(ctx, &PostBaseParams{IdempotencyKey: Key()}, body))
}

// BaseOf answers the base a finished base operation (pull or build) produced, by the digest in its result.
func (c *Workspace) BaseOf(ctx context.Context, op *BaseOperation) (*BaseBase, error) {
	if op.Result == nil {
		return nil, fmt.Errorf("operation %s has no result", op.Id)
	}
	digest, _ := (*op.Result)["digest"].(string)
	return c.Base(ctx, digest)
}

// BuildBase posts a build context archive (tar, tar.gz or zip with a Dockerfile) and answers the BUILD_BASE operation.
// size sets the Content-Length, a file body would go chunked otherwise.
func (c *Workspace) BuildBase(ctx context.Context, name string, body io.Reader, size int64) (*BaseOperation, error) {
	p := PostBaseBuildParams{}
	if name != "" {
		p.Name = &name
	}
	return ok[BaseOperation](c.gen.PostBaseBuildWithBody(ctx, &p, "application/octet-stream", body, func(_ context.Context, req *http.Request) error {
		req.ContentLength = size
		return nil
	}))
}

func (c *Workspace) BuildCache(ctx context.Context) (*ResponseBaseBuildCache, error) {
	return ok[ResponseBaseBuildCache](c.gen.GetBaseBuildCache(ctx))
}

func (c *Workspace) PruneBuildCache(ctx context.Context) error {
	return done(c.gen.DeleteBaseBuildCache(ctx))
}

func (c *Workspace) UpdateBase(ctx context.Context, digest string, body RequestPutBase) (*BaseBase, error) {
	return ok[BaseBase](c.gen.PutBase(ctx, digest, body))
}

func (c *Workspace) Bases(ctx context.Context) ([]BaseBase, error) {
	return items[BaseBase](c.gen.GetBaseList(ctx))
}

func (c *Workspace) Base(ctx context.Context, digest string) (*BaseBase, error) {
	return ok[BaseBase](c.gen.GetBase(ctx, digest))
}

func (c *Workspace) DeleteBase(ctx context.Context, digest string) error {
	return done(c.gen.DeleteBase(ctx, digest))
}

func (c *Workspace) CreateTemplate(ctx context.Context, body RequestPostTemplate) (*BaseTemplate, error) {
	return ok[BaseTemplate](c.gen.PostTemplate(ctx, &PostTemplateParams{IdempotencyKey: Key()}, body))
}

func (c *Workspace) Templates(ctx context.Context) ([]BaseTemplate, error) {
	return items[BaseTemplate](c.gen.GetTemplateList(ctx))
}

func (c *Workspace) Template(ctx context.Context, digest string) (*BaseTemplate, error) {
	return ok[BaseTemplate](c.gen.GetTemplate(ctx, digest))
}

func (c *Workspace) UpdateTemplate(ctx context.Context, digest string, body RequestPutTemplate) (*BaseTemplate, error) {
	return ok[BaseTemplate](c.gen.PutTemplate(ctx, digest, body))
}

func (c *Workspace) DeleteTemplate(ctx context.Context, digest string) error {
	return done(c.gen.DeleteTemplate(ctx, digest))
}

// ---- sandbox lifecycle

// CreateSandbox answers like fork and restore: the operation and the one new row, in CREATING until the operation finishes.
func (c *Workspace) CreateSandbox(ctx context.Context, body RequestPostSandbox) (*ResponseSpawn, error) {
	if body.Integrations == nil {
		body.Integrations = []InDtoBodyPostIntegrationAttachment{}
	}
	return ok[ResponseSpawn](c.gen.PostSandbox(ctx, &PostSandboxParams{IdempotencyKey: Key()}, body))
}

func (c *Workspace) Sandboxes(ctx context.Context, p GetSandboxListParams) ([]BaseSandbox, error) {
	return items[BaseSandbox](c.gen.GetSandboxList(ctx, &p))
}

func (c *Workspace) Sandbox(ctx context.Context, id string) (*BaseSandbox, error) {
	return ok[BaseSandbox](c.gen.GetSandbox(ctx, id))
}

func (c *Workspace) UpdateSandbox(ctx context.Context, id string, body RequestPutSandbox) (*BaseSandbox, error) {
	return ok[BaseSandbox](c.gen.PutSandbox(ctx, id, body))
}

func (c *Workspace) DeleteSandbox(ctx context.Context, id string) (*BaseOperation, error) {
	return ok[BaseOperation](c.gen.DeleteSandbox(ctx, id, &DeleteSandboxParams{IdempotencyKey: Key()}))
}

func (c *Workspace) Park(ctx context.Context, id string) (*BaseOperation, error) {
	return ok[BaseOperation](c.gen.PostSandboxPark(ctx, id, &PostSandboxParkParams{IdempotencyKey: Key()}))
}

func (c *Workspace) Wake(ctx context.Context, id string, redeliverSecrets bool) (*BaseOperation, error) {
	body := RequestPostSandboxWake{}
	if redeliverSecrets {
		body.RedeliverSecrets = ptr(true)
	}
	return ok[BaseOperation](c.gen.PostSandboxWake(ctx, id, &PostSandboxWakeParams{IdempotencyKey: Key()}, body))
}

func (c *Workspace) Fork(ctx context.Context, id string, body RequestPostSandboxFork) (*ResponseSpawn, error) {
	return ok[ResponseSpawn](c.gen.PostSandboxFork(ctx, id, &PostSandboxForkParams{IdempotencyKey: Key()}, body))
}

func (c *Workspace) PutPolicy(ctx context.Context, id string, generation int, policy BaseSandboxPolicy) (*BaseSandbox, error) {
	return ok[BaseSandbox](c.gen.PutSandboxPolicy(ctx, id, RequestPutSandboxPolicy{Policy: policy, PolicyGeneration: generation}))
}

func (c *Workspace) Ssh(ctx context.Context, id string) (*ResponseSandboxSsh, error) {
	return ok[ResponseSandboxSsh](c.gen.GetSandboxSsh(ctx, id))
}

func (c *Workspace) AddSshKey(ctx context.Context, id, publicKey string) error {
	return done(c.gen.PostSandboxSshKey(ctx, id, RequestPostSshKey{PublicKey: publicKey}))
}

func (c *Workspace) Metrics(ctx context.Context, id string, p GetSandboxMetricListParams) ([]BaseMetricHourly, error) {
	return items[BaseMetricHourly](c.gen.GetSandboxMetricList(ctx, id, &p))
}

// ---- operations and events

func (c *Workspace) Operations(ctx context.Context, p GetOperationListParams) ([]BaseOperation, error) {
	return items[BaseOperation](c.gen.GetOperationList(ctx, &p))
}

func (c *Workspace) Operation(ctx context.Context, id string) (*BaseOperation, error) {
	return ok[BaseOperation](c.gen.GetOperation(ctx, id, &GetOperationParams{}))
}

// operationWaitSeconds is the orchestrator's cap on one held answer.
const operationWaitSeconds = 30

func (c *Workspace) Events(ctx context.Context, p GetEventListParams) ([]BaseEvent, error) {
	return items[BaseEvent](c.gen.GetEventList(ctx, &p))
}

func (c *Workspace) SandboxEvents(ctx context.Context, id string, p GetSandboxEventListParams) ([]BaseEvent, error) {
	return items[BaseEvent](c.gen.GetSandboxEventList(ctx, id, &p))
}

// OperationFailed is the error of an operation that ended FAILED, with the message the orchestrator gave.
type OperationFailed struct{ Op BaseOperation }

func (e OperationFailed) Error() string {
	msg := "no reason given"
	if e.Op.Error != nil && *e.Op.Error != "" {
		msg = *e.Op.Error
	}
	return fmt.Sprintf("%s failed: %s", strings.ToLower(string(e.Op.Kind)), msg)
}

// WaitOperation long-polls until the operation ends, and turns FAILED into an error so `--wait` commands exit non-zero.
func (c *Workspace) WaitOperation(ctx context.Context, id string) (*BaseOperation, error) {
	wait := operationWaitSeconds
	for {
		op, err := ok[BaseOperation](c.gen.GetOperation(ctx, id, &GetOperationParams{WaitSeconds: &wait}))
		if err != nil {
			return nil, err
		}
		switch op.Status {
		case BaseOperationStatusFINISHED:
			return op, nil
		case BaseOperationStatusFAILED:
			return op, OperationFailed{Op: *op}
		}
		select {
		case <-ctx.Done():
			return op, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ---- snapshots, lineage, export

func (c *Workspace) Snapshot(ctx context.Context, id string) (*BaseSnapshot, error) {
	return ok[BaseSnapshot](c.gen.GetSnapshot(ctx, id))
}

func (c *Workspace) Snapshots(ctx context.Context, sandboxId string) ([]BaseSnapshot, error) {
	return items[BaseSnapshot](c.gen.GetSandboxSnapshotList(ctx, sandboxId))
}

func (c *Workspace) TakeSnapshot(ctx context.Context, sandboxId string, body RequestPostSandboxSnapshot) (*BaseOperation, error) {
	return ok[BaseOperation](c.gen.PostSandboxSnapshot(ctx, sandboxId, &PostSandboxSnapshotParams{IdempotencyKey: Key()}, body))
}

func (c *Workspace) DeleteSnapshot(ctx context.Context, id string) error {
	return done(c.gen.DeleteSnapshot(ctx, id))
}

func (c *Workspace) Restore(ctx context.Context, id string, body RequestPostSnapshotRestore) (*ResponseSpawn, error) {
	return ok[ResponseSpawn](c.gen.PostSnapshotRestore(ctx, id, &PostSnapshotRestoreParams{IdempotencyKey: Key()}, body))
}

func (c *Workspace) Lineages(ctx context.Context) ([]BaseLineage, error) {
	return items[BaseLineage](c.gen.GetLineageList(ctx))
}

func (c *Workspace) Lineage(ctx context.Context, id string) (*BaseLineage, error) {
	return ok[BaseLineage](c.gen.GetLineage(ctx, id))
}

func (c *Workspace) UpdateLineage(ctx context.Context, id string, exportEnabled bool) (*BaseLineage, error) {
	return ok[BaseLineage](c.gen.PutLineage(ctx, id, RequestPutLineage{ExportEnabled: exportEnabled}))
}

func (c *Workspace) ExportLineage(ctx context.Context, id string, destination string) (*BaseExport, error) {
	body := RequestPostLineageExport{}
	if destination != "" {
		body.Destination = &destination
	}
	return ok[BaseExport](c.gen.PostLineageExport(ctx, id, &PostLineageExportParams{IdempotencyKey: Key()}, body))
}

func (c *Workspace) Exports(ctx context.Context, lineageId string) ([]BaseExport, error) {
	p := GetExportListParams{}
	if lineageId != "" {
		p.LineageId = &lineageId
	}
	return items[BaseExport](c.gen.GetExportList(ctx, &p))
}

func (c *Workspace) Export(ctx context.Context, id string) (*BaseExport, error) {
	return ok[BaseExport](c.gen.GetExport(ctx, id))
}

func (c *Workspace) Import(ctx context.Context, body RequestPostImport) (*BaseOperation, error) {
	return ok[BaseOperation](c.gen.PostImport(ctx, &PostImportParams{IdempotencyKey: Key()}, body))
}

// ---- ingress credentials

func (c *Workspace) IssueCredential(ctx context.Context, sandboxId string, body RequestPostIngressCredential) (*ResponseIngressCredentialIssued, error) {
	return ok[ResponseIngressCredentialIssued](c.gen.PostSandboxIngressCredential(ctx, sandboxId, body))
}

func (c *Workspace) Credentials(ctx context.Context, sandboxId string, includeRevoked bool) ([]BaseIngressCredential, error) {
	p := GetSandboxIngressCredentialListParams{}
	if includeRevoked {
		p.IncludeRevoked = ptr(true)
	}
	return items[BaseIngressCredential](c.gen.GetSandboxIngressCredentialList(ctx, sandboxId, &p))
}

func (c *Workspace) RevokeCredential(ctx context.Context, id string) error {
	return done(c.gen.DeleteIngressCredential(ctx, id))
}

// ---- integrations and webhooks

func (c *Workspace) CreateIntegration(ctx context.Context, body RequestPostIntegration) (*BaseIntegration, error) {
	return ok[BaseIntegration](c.gen.PostIntegration(ctx, body))
}

func (c *Workspace) Integrations(ctx context.Context) ([]BaseIntegration, error) {
	return items[BaseIntegration](c.gen.GetIntegrationList(ctx))
}

func (c *Workspace) Integration(ctx context.Context, name string) (*BaseIntegration, error) {
	return ok[BaseIntegration](c.gen.GetIntegration(ctx, name))
}

func (c *Workspace) UpdateIntegration(ctx context.Context, name string, body RequestPutIntegration) (*BaseIntegration, error) {
	return ok[BaseIntegration](c.gen.PutIntegration(ctx, name, body))
}

func (c *Workspace) DeleteIntegration(ctx context.Context, name string) error {
	return done(c.gen.DeleteIntegration(ctx, name))
}

func (c *Workspace) Attach(ctx context.Context, sandboxId string, body RequestPostIntegrationAttachment) (*BaseIntegrationAttachment, error) {
	return ok[BaseIntegrationAttachment](c.gen.PostSandboxIntegrationAttachment(ctx, sandboxId, body))
}

func (c *Workspace) Attachments(ctx context.Context, sandboxId string) ([]BaseIntegrationAttachment, error) {
	return items[BaseIntegrationAttachment](c.gen.GetSandboxIntegrationAttachmentList(ctx, sandboxId))
}

func (c *Workspace) Detach(ctx context.Context, sandboxId, name string) error {
	return done(c.gen.DeleteSandboxIntegrationAttachment(ctx, sandboxId, name))
}

// Connectors are the fixed catalogue of provider logins (claude, github, codex) whose credential lives in a Workspace env and is injected by the credential proxy.
func (c *Workspace) Connectors(ctx context.Context) ([]BaseConnector, error) {
	return items[BaseConnector](c.gen.GetConnectorList(ctx))
}

func (c *Workspace) Connector(ctx context.Context, name string) (*BaseConnector, error) {
	return ok[BaseConnector](c.gen.GetConnector(ctx, name))
}

func (c *Workspace) SetConnectorCredential(ctx context.Context, name string, body RequestPutConnectorCredential) (*BaseConnector, error) {
	return ok[BaseConnector](c.gen.PutConnectorCredential(ctx, name, body))
}

// ClearConnectorCredential blanks the env, the connector's integrations stay.
func (c *Workspace) ClearConnectorCredential(ctx context.Context, name string) (*BaseConnector, error) {
	return ok[BaseConnector](c.gen.DeleteConnectorCredential(ctx, name))
}

// ConnectorLogin starts the provider's login command in a helper sandbox and answers the detached PTY session to attach to.
func (c *Workspace) ConnectorLogin(ctx context.Context, name string) (*ResponseConnectorLogin, error) {
	return ok[ResponseConnectorLogin](c.gen.PostConnectorLogin(ctx, name))
}

func (c *Workspace) CreateWebhook(ctx context.Context, body RequestPostWebhook) (*ResponseWebhookCreated, error) {
	return ok[ResponseWebhookCreated](c.gen.PostWebhook(ctx, body))
}

func (c *Workspace) Webhooks(ctx context.Context) ([]BaseWebhookEndpoint, error) {
	return items[BaseWebhookEndpoint](c.gen.GetWebhookList(ctx))
}

func (c *Workspace) Webhook(ctx context.Context, id string) (*BaseWebhookEndpoint, error) {
	return ok[BaseWebhookEndpoint](c.gen.GetWebhook(ctx, id))
}

func (c *Workspace) UpdateWebhook(ctx context.Context, id string, body RequestPutWebhook) (*BaseWebhookEndpoint, error) {
	return ok[BaseWebhookEndpoint](c.gen.PutWebhook(ctx, id, body))
}

func (c *Workspace) DeleteWebhook(ctx context.Context, id string) error {
	return done(c.gen.DeleteWebhook(ctx, id))
}

func (c *Workspace) WebhookDeliveries(ctx context.Context, id string, p GetWebhookDeliveryListParams) ([]BaseWebhookDelivery, error) {
	return items[BaseWebhookDelivery](c.gen.GetWebhookDeliveryList(ctx, id, &p))
}

// ---- quarantine

func (c *Workspace) Quarantine(ctx context.Context) ([]BaseQuarantine, error) {
	return items[BaseQuarantine](c.gen.GetQuarantineList(ctx))
}

func (c *Workspace) DeleteQuarantine(ctx context.Context, id string) error {
	return done(c.gen.DeleteQuarantine(ctx, id))
}

// ---- guest: exec, processes, sessions, files

func (c *Workspace) Exec(ctx context.Context, id string, body RequestPostExec) (*ResponseExecResult, error) {
	return ok[ResponseExecResult](c.gen.PostSandboxExec(ctx, id, body))
}

func (c *Workspace) StartProcess(ctx context.Context, id string, body RequestPostExec) (*BaseGuestSession, error) {
	return ok[BaseGuestSession](c.gen.PostSandboxProcess(ctx, id, body))
}

func (c *Workspace) Sessions(ctx context.Context, id string, p GetSandboxSessionListParams) ([]BaseGuestSession, error) {
	return items[BaseGuestSession](c.gen.GetSandboxSessionList(ctx, id, &p))
}

func (c *Workspace) Session(ctx context.Context, id string) (*BaseGuestSession, error) {
	return ok[BaseGuestSession](c.gen.GetSession(ctx, id))
}

func (c *Workspace) Interrupt(ctx context.Context, id string) error {
	return done(c.gen.PostSessionInterrupt(ctx, id))
}

func (c *Workspace) SessionOutput(ctx context.Context, id string) (io.ReadCloser, error) {
	return stream(c.gen.GetSessionOutput(ctx, id))
}

func (c *Workspace) Files(ctx context.Context, id, path string) ([]BaseFileEntry, error) {
	return items[BaseFileEntry](c.gen.GetSandboxFileList(ctx, id, &GetSandboxFileListParams{Path: path}))
}

func (c *Workspace) ReadFile(ctx context.Context, id, path string) (io.ReadCloser, error) {
	return stream(c.gen.GetSandboxFile(ctx, id, &GetSandboxFileParams{Path: path}))
}

func (c *Workspace) ReadArchive(ctx context.Context, id, path string) (io.ReadCloser, error) {
	return stream(c.gen.GetSandboxFileArchive(ctx, id, &GetSandboxFileArchiveParams{Path: path}))
}

func (c *Workspace) WriteFile(ctx context.Context, id, path, mode string, body io.Reader) error {
	p := PutSandboxFileParams{Path: path}
	if mode != "" {
		p.Mode = &mode
	}
	return done(c.gen.PutSandboxFileWithBody(ctx, id, &p, "application/octet-stream", body))
}

func (c *Workspace) DeleteFile(ctx context.Context, id, path string) error {
	return done(c.gen.DeleteSandboxFile(ctx, id, &DeleteSandboxFileParams{Path: path}))
}

// ---- Console routes
//
// The console is served by the orchestrator at / of the API host and its routes are registered by hand on its mux, outside the OpenAPI contract.

// ConsoleTicket is a single-use console sign-in ticket, minted by POST /console/login-ticket. It lives 60 s.
type ConsoleTicket struct {
	Ticket  string `json:"ticket"`
	Expires string `json:"expires"`
}

// ConsoleLoginTicket mints a single-use ticket for /console/login?ticket=, so the workspace API key never appears in a URL. An orchestrator from before 2026-09-24 has no such route and answers 404.
func (c *Workspace) ConsoleLoginTicket(ctx context.Context) (*ConsoleTicket, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseUrl+"/console/login-ticket", nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return ok[ConsoleTicket](c.http.Do(req))
}

// ---- WebSocket routes

// wsUrl turns the API base into the WebSocket form of one route with its query.
func (c *Workspace) wsUrl(p string, q url.Values) string {
	u := c.BaseUrl
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	u += p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// dial opens a WebSocket route with the bearer on the upgrade request. A refused upgrade carries the API error body, which is surfaced like any other.
func (c *Workspace) dial(ctx context.Context, p string, q url.Values) (*websocket.Conn, error) {
	h := http.Header{}
	if c.apiKey != "" {
		h.Set("Authorization", "Bearer "+c.apiKey)
	}
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, c.wsUrl(p, q), h)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return nil, decodeError(resp, body)
		}
		return nil, err
	}
	return conn, nil
}

// Shell opens the PTY of a sandbox. Binary frames carry terminal bytes both ways, a text frame {"resize":{"cols":N,"rows":N}} resizes the pty.
func (c *Workspace) Shell(ctx context.Context, id string, cols, rows int, sessionId, ticket string) (*websocket.Conn, error) {
	q := url.Values{"cols": {fmt.Sprint(cols)}, "rows": {fmt.Sprint(rows)}}
	if sessionId != "" {
		q.Set("sessionId", sessionId)
	}
	if ticket != "" {
		q.Set("sessionTicket", ticket)
	}
	return c.dial(ctx, "/api/rest/sandbox/"+id+"/shell", q)
}

// StreamEvents follows the event stream from the cursor on, one JSON text frame per event, until the callback errors or the context ends.
func (c *Workspace) StreamEvents(ctx context.Context, afterId *int64, sandboxId string, fn func(BaseEvent) error) error {
	q := url.Values{}
	if afterId != nil {
		q.Set("afterId", fmt.Sprint(*afterId))
	}
	if sandboxId != "" {
		q.Set("sandboxId", sandboxId)
	}
	conn, err := c.dial(ctx, "/api/rest/event-stream", q)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		conn.Close()
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			// An interrupt is how a follow ends, not a failure to report.
			if ctx.Err() != nil {
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return nil
			}
			return err
		}
		var ev BaseEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return errors.New("invalid event frame: " + err.Error())
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
}
