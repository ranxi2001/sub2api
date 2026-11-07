package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// M0 never forwards production mutations. These four routes only read reports
// or request a bounded, potentially billable diagnosis in the companion service.
const aiOpsResponseLimit = 2 << 20

type aiOpsProxy struct {
	baseURL string
	token   string
	client  *http.Client
	invalid bool
}

type aiOpsStatus struct {
	Enabled                  bool   `json:"enabled"`
	Mode                     string `json:"mode"`
	Configured               bool   `json:"configured"`
	Running                  bool   `json:"running"`
	GroupID                  *int64 `json:"group_id"`
	WindowMinutes            int    `json:"window_minutes"`
	MinIntervalSeconds       int    `json:"min_interval_seconds"`
	DailyRunLimit            int    `json:"daily_run_limit"`
	AutomaticIntervalSeconds int    `json:"automatic_interval_seconds"`
	LastRunAt                string `json:"last_run_at,omitempty"`
}

type aiOpsWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type aiOpsEvidence struct {
	ID          string         `json:"id"`
	Source      string         `json:"source"`
	Status      string         `json:"status"`
	CollectedAt string         `json:"collected_at"`
	Metrics     map[string]any `json:"metrics"`
}

type aiOpsFinding struct {
	Severity       string   `json:"severity"`
	Category       string   `json:"category"`
	Description    string   `json:"description"`
	Recommendation string   `json:"recommendation"`
	EvidenceIDs    []string `json:"evidence_ids"`
	AccountIDs     []int64  `json:"account_ids"`
}

type aiOpsProposal struct {
	Action        string   `json:"action"`
	AccountID     *int64   `json:"account_id"`
	ProposedValue *float64 `json:"proposed_value"`
	Reason        string   `json:"reason"`
	EvidenceIDs   []string `json:"evidence_ids"`
	Executable    bool     `json:"executable"`
}

type aiOpsResult struct {
	Summary     string          `json:"summary"`
	Findings    []aiOpsFinding  `json:"findings"`
	Proposals   []aiOpsProposal `json:"proposals"`
	Limitations []string        `json:"limitations"`
}

type aiOpsRun struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	CreatedAt  string          `json:"created_at"`
	FinishedAt string          `json:"finished_at,omitempty"`
	GroupID    int64           `json:"group_id"`
	Window     aiOpsWindow     `json:"window"`
	Evidence   []aiOpsEvidence `json:"evidence"`
	Result     *aiOpsResult    `json:"result,omitempty"`
	ErrorCode  string          `json:"error_code,omitempty"`
}

type aiOpsRunList struct {
	Items []aiOpsRun `json:"items"`
	Total int        `json:"total"`
}

func newAIOpsProxyFromEnv() *aiOpsProxy {
	raw := strings.TrimSpace(os.Getenv("SUB2API_AI_OPS_URL"))
	token := strings.TrimSpace(os.Getenv("SUB2API_AI_OPS_TOKEN"))
	if raw == "" && token == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Scheme != "http" || parsed.User != nil ||
		(parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || len(token) < 32 {
		return &aiOpsProxy{invalid: true}
	}
	return &aiOpsProxy{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		token:   token,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
				ResponseHeaderTimeout: 5 * time.Second,
				MaxConnsPerHost:       4,
				IdleConnTimeout:       30 * time.Second,
			},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func allowAIOps(c *gin.Context) bool {
	role, ok := middleware.GetUserRoleFromContext(c)
	_, observer := service.ObserverGroupIDs(c.Request.Context())
	if !ok || role != "admin" || observer || adminaccess.FromContext(c.Request.Context()) != nil {
		response.Error(c, http.StatusForbidden, "AI operations requires full administrator access")
		return false
	}
	// No caller-controlled URL, scope, pagination or instructions reach the agent.
	if c.Request.URL.RawQuery != "" {
		response.BadRequest(c, "AI operations does not accept query parameters")
		return false
	}
	c.Header("Cache-Control", "no-store")
	return true
}

func (p *aiOpsProxy) request(ctx context.Context, method, path string, out any) (int, error) {
	if p == nil || p.invalid {
		return http.StatusServiceUnavailable, errors.New("AI operations is not configured")
	}
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return http.StatusBadGateway, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return http.StatusBadGateway, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusNotFound {
			return resp.StatusCode, errors.New("AI operations request could not be completed")
		}
		return http.StatusBadGateway, errors.New("AI operations service unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, aiOpsResponseLimit+1))
	if err != nil || len(raw) > aiOpsResponseLimit {
		return http.StatusBadGateway, errors.New("invalid AI operations response")
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return http.StatusBadGateway, errors.New("invalid AI operations response")
	}
	return resp.StatusCode, nil
}

func aiOpsProxyError(c *gin.Context, status int) {
	// Never expose internal addresses, authorization headers or upstream bodies.
	response.Error(c, status, "AI operations is unavailable, busy or its request budget has been reached")
}

func (h *OpsHandler) GetAIOpsStatus(c *gin.Context) {
	if !allowAIOps(c) {
		return
	}
	if h.aiOps == nil {
		response.Success(c, aiOpsStatus{Mode: "observe", WindowMinutes: 15, MinIntervalSeconds: 300, DailyRunLimit: 24})
		return
	}
	var result aiOpsStatus
	status, err := h.aiOps.request(c.Request.Context(), http.MethodGet, "/v1/status", &result)
	if err != nil || result.Mode != "observe" {
		aiOpsProxyError(c, aiOpsErrorStatus(status, err))
		return
	}
	response.Success(c, result)
}

func (h *OpsHandler) ListAIOpsRuns(c *gin.Context) {
	if !allowAIOps(c) {
		return
	}
	if h.aiOps == nil {
		response.Success(c, aiOpsRunList{Items: []aiOpsRun{}})
		return
	}
	var result aiOpsRunList
	status, err := h.aiOps.request(c.Request.Context(), http.MethodGet, "/v1/runs", &result)
	if err != nil || len(result.Items) > 20 || result.Total < 0 || result.Total > 20 {
		aiOpsProxyError(c, aiOpsErrorStatus(status, err))
		return
	}
	for i := range result.Items {
		if !validAIOpsRun(&result.Items[i]) {
			aiOpsProxyError(c, http.StatusBadGateway)
			return
		}
		result.Items[i].Result = nil
		result.Items[i].Evidence = []aiOpsEvidence{}
	}
	response.Success(c, result)
}

func (h *OpsHandler) GetAIOpsRun(c *gin.Context) {
	if !allowAIOps(c) {
		return
	}
	id := c.Param("id")
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		response.BadRequest(c, "Invalid AI operations run ID")
		return
	}
	var result aiOpsRun
	status, err := h.aiOps.request(c.Request.Context(), http.MethodGet, "/v1/runs/"+id, &result)
	if err != nil || result.ID != id || !validAIOpsRun(&result) {
		aiOpsProxyError(c, aiOpsErrorStatus(status, err))
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"source": "codex_sdk", "event_id": result.ID})
	normalizeAIOpsRun(&result)
	response.Success(c, result)
}

func (h *OpsHandler) CreateAIOpsRun(c *gin.Context) {
	if !allowAIOps(c) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	var input map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &input) != nil || input == nil || len(input) != 0 || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		response.BadRequest(c, "Only an empty JSON object is accepted")
		return
	}
	var result aiOpsRun
	status, err := h.aiOps.request(c.Request.Context(), http.MethodPost, "/v1/runs", &result)
	if err != nil || !validAIOpsRun(&result) {
		aiOpsProxyError(c, aiOpsErrorStatus(status, err))
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"source": "codex_sdk", "event_id": result.ID})
	normalizeAIOpsRun(&result)
	response.Accepted(c, result)
}

func aiOpsErrorStatus(status int, err error) int {
	if err == nil {
		return http.StatusBadGateway
	}
	return status
}

func validAIOpsRun(run *aiOpsRun) bool {
	if _, err := uuid.Parse(run.ID); err != nil || run.GroupID <= 0 || len(run.Evidence) > 16 {
		return false
	}
	switch run.Status {
	case "queued", "collecting", "analyzing", "completed", "failed", "interrupted":
	default:
		return false
	}
	if run.Result != nil {
		if len(run.Result.Findings) > 12 || len(run.Result.Proposals) > 8 || len(run.Result.Summary) > 8000 {
			return false
		}
		for _, proposal := range run.Result.Proposals {
			if proposal.Executable {
				return false
			}
		}
	}
	return true
}

func normalizeAIOpsRun(run *aiOpsRun) {
	if run.Evidence == nil {
		run.Evidence = []aiOpsEvidence{}
	}
	if run.Result == nil {
		return
	}
	if run.Result.Findings == nil {
		run.Result.Findings = []aiOpsFinding{}
	}
	if run.Result.Proposals == nil {
		run.Result.Proposals = []aiOpsProposal{}
	}
	if run.Result.Limitations == nil {
		run.Result.Limitations = []string{}
	}
	for i := range run.Result.Findings {
		if run.Result.Findings[i].EvidenceIDs == nil {
			run.Result.Findings[i].EvidenceIDs = []string{}
		}
		if run.Result.Findings[i].AccountIDs == nil {
			run.Result.Findings[i].AccountIDs = []int64{}
		}
	}
	for i := range run.Result.Proposals {
		if run.Result.Proposals[i].EvidenceIDs == nil {
			run.Result.Proposals[i].EvidenceIDs = []string{}
		}
	}
}
