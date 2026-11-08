package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type aiOpsControlRepoStub struct {
	service.AccountRepository
	service.AIOpsRepository
	policy service.AIOpsPolicy
	calls  int
	input  service.AIOpsPreviewInput
}

func (r *aiOpsControlRepoStub) AIOpsPolicy(context.Context) (service.AIOpsPolicy, error) {
	return r.policy, nil
}
func (r *aiOpsControlRepoStub) PreviewAIOps(_ context.Context, input service.AIOpsPreviewInput, _ int64) (service.AIOpsProposal, error) {
	r.calls++
	r.input = input
	return service.AIOpsProposal{ID: aiOpsTestID, Action: input.Action, AccountID: input.AccountID, Status: "blocked", BlockedReasons: []string{"capacity_floor"}}, nil
}
func (r *aiOpsControlRepoStub) ApplyAIOps(context.Context, string, string, int64, bool) (service.AIOpsAction, error) {
	r.calls++
	return service.AIOpsAction{}, service.ErrAIOpsConflict
}

func newAIOpsControlRouter(repo *aiOpsControlRepoStub) (*gin.Engine, *OpsHandler) {
	svc := service.NewOpsService(nil, nil, nil, repo, nil, nil, nil, nil, nil, nil, nil)
	h := &OpsHandler{opsService: svc}
	router := aiOpsTestRouter(h, "admin", nil)
	router.POST("/preview", h.PreviewAIOpsProposal)
	router.POST("/apply/:id", h.ApplyAIOpsProposal)
	router.POST("/cancel/:id", h.CancelAIOpsProposal)
	return router, h
}

func TestAIOpsApplyRequiresHostGateAndExactConfirmation(t *testing.T) {
	repo := &aiOpsControlRepoStub{}
	router, _ := newAIOpsControlRouter(repo)
	t.Setenv("SUB2API_AI_OPS_CONTROL_ENABLED", "false")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/apply/"+aiOpsTestID, strings.NewReader(`{"digest":"`+strings.Repeat("a", 64)+`","confirm":true}`)))
	if w.Code != http.StatusServiceUnavailable || repo.calls != 0 {
		t.Fatal("host gate allowed a write")
	}
	t.Setenv("SUB2API_AI_OPS_CONTROL_ENABLED", "true")
	for _, body := range []string{`{"digest":"bad","confirm":true}`, `{"digest":"` + strings.Repeat("a", 64) + `","confirm":false}`, `{"digest":"` + strings.Repeat("a", 64) + `","confirm":true,"account_id":1}`} {
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/apply/"+aiOpsTestID, strings.NewReader(body)))
		if w.Code != http.StatusBadRequest || repo.calls != 0 {
			t.Fatal("malformed confirmation reached repository")
		}
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/apply/"+aiOpsTestID, strings.NewReader(`{"digest":"`+strings.Repeat("a", 64)+`","confirm":true}`)))
	if w.Code != http.StatusConflict || repo.calls != 1 {
		t.Fatal("repository conflict was not preserved")
	}
}

func TestAIOpsPreviewBindsCandidateFetchedFromCompanion(t *testing.T) {
	t.Setenv("SUB2API_AI_OPS_CONTROL_ENABLED", "true")
	repo := &aiOpsControlRepoStub{policy: service.AIOpsPolicy{GroupID: 7}}
	router, h := newAIOpsControlRouter(repo)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runs/"+aiOpsTestID {
			t.Error("unexpected companion request")
		}
		_, _ = w.Write([]byte(`{"id":"` + aiOpsTestID + `","status":"completed","group_id":7,"window":{"end":"2026-11-05T05:00:00Z"},"evidence":[{"id":"accounts","status":"ok"}],"result":{"proposals":[{"action":"set_concurrency","account_id":42,"proposed_value":9,"executable":false}]}}`))
	}))
	defer server.Close()
	h.aiOps = &aiOpsProxy{baseURL: server.URL, token: "internal-only", client: server.Client()}
	for _, body := range []string{`{"run_id":"` + aiOpsTestID + `","proposal_index":0,"account_id":99}`, `{"run_id":"` + aiOpsTestID + `","proposal_index":0,"proposed_value":9999}`} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest || repo.calls != 0 {
			t.Fatal("caller overrode immutable candidate")
		}
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(`{"run_id":"`+aiOpsTestID+`","proposal_index":0}`)))
	if w.Code != http.StatusOK || repo.input.AccountID != 42 || repo.input.ProposedValue == nil || *repo.input.ProposedValue != 9 {
		t.Fatal("candidate binding failed")
	}
}

func TestAIOpsControlErrorDoesNotExposePersistenceFailures(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	aiOpsControlError(c, errors.New("secret-connection-and-sql"))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("persistence error leaked")
	}
}

func TestAIOpsControlRoutesRequireFullAdmin(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost} {
		router := gin.New()
		router.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), "observer"); c.Next() })
		h := &OpsHandler{}
		router.Handle(method, "/control", h.SaveAIOpsPolicy)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/control", strings.NewReader("{}")))
		if w.Code != http.StatusForbidden {
			t.Fatal("non-admin reached control handler")
		}
	}
}

func (r *aiOpsControlRepoStub) CancelAIOpsProposal(context.Context, string, string, int64) (service.AIOpsProposal, error) {
	r.calls++
	return service.AIOpsProposal{ID: aiOpsTestID, Status: "cancelled"}, nil
}

func TestAIOpsCancelNeedsFullAdminAndExactDigestButNotExecutionGate(t *testing.T) {
	t.Setenv("SUB2API_AI_OPS_CONTROL_ENABLED", "false")
	repo := &aiOpsControlRepoStub{}
	router, h := newAIOpsControlRouter(repo)
	for _, body := range []string{`{}`, `{"digest":"bad"}`, `{"digest":"` + strings.Repeat("a", 64) + `","account_id":1}`} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/cancel/"+aiOpsTestID, strings.NewReader(body)))
		if w.Code != http.StatusBadRequest || repo.calls != 0 {
			t.Fatal("malformed cancellation reached repository")
		}
	}
	body := `{"digest":"` + strings.Repeat("a", 64) + `"}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/cancel/"+aiOpsTestID, strings.NewReader(body)))
	if w.Code != http.StatusOK || repo.calls != 1 {
		t.Fatal("execution gate blocked safe proposal cancellation")
	}
	observer := aiOpsTestRouter(h, "observer", nil)
	observer.POST("/cancel/:id", h.CancelAIOpsProposal)
	w = httptest.NewRecorder()
	observer.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/cancel/"+aiOpsTestID, strings.NewReader(body)))
	if w.Code != http.StatusForbidden || repo.calls != 1 {
		t.Fatal("observer cancelled a proposal")
	}
}
