package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const aiOpsTestID = "7f9565eb-6cd7-4b17-b8b6-0cdb1b3c0725"

func aiOpsTestRouter(h *OpsHandler, role string, transform func(context.Context) context.Context) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUserRole), role)
		if transform != nil {
			c.Request = c.Request.WithContext(transform(c.Request.Context()))
		}
		c.Next()
	})
	router.GET("/status", h.GetAIOpsStatus)
	router.GET("/runs", h.ListAIOpsRuns)
	router.GET("/runs/:id", h.GetAIOpsRun)
	router.POST("/runs", h.CreateAIOpsRun)
	return router
}

func TestAIOpsDisabledHasNoExternalSideEffects(t *testing.T) {
	h := &OpsHandler{}
	router := aiOpsTestRouter(h, "admin", nil)
	for _, path := range []string{"/status", "/runs"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s returned %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/runs", strings.NewReader("{}")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled run returned %d", w.Code)
	}
}

func TestAIOpsRejectsNonAdminObserverAndScopedKeys(t *testing.T) {
	for _, tc := range []struct {
		name      string
		role      string
		transform func(context.Context) context.Context
	}{
		{name: "user", role: "user"},
		{name: "anonymous"},
		{name: "observer", role: "admin", transform: func(ctx context.Context) context.Context { return service.WithObserverScope(ctx, []int64{1}) }},
		{name: "scoped key", role: "admin", transform: func(ctx context.Context) context.Context {
			return adminaccess.WithPrincipal(ctx, &adminaccess.Principal{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := aiOpsTestRouter(&OpsHandler{}, tc.role, tc.transform)
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(method, "/runs", strings.NewReader("{}")))
				if w.Code != http.StatusForbidden {
					t.Fatalf("%s returned %d", method, w.Code)
				}
			}
		})
	}
}

func TestAIOpsConfigOnlyAllowsLiteralLoopback(t *testing.T) {
	t.Setenv("SUB2API_AI_OPS_TOKEN", strings.Repeat("x", 32))
	for _, value := range []string{"http://example.invalid", "http://localhost:17862", "http://127.0.0.1:17862/path", "http://user@127.0.0.1", "http://127.0.0.1?token=secret", "http://127.0.0.1#fragment"} {
		t.Setenv("SUB2API_AI_OPS_URL", value)
		p := newAIOpsProxyFromEnv()
		if p == nil || !p.invalid {
			t.Fatalf("unsafe configuration accepted: %s", value)
		}
	}
}

func TestAIOpsRejectsInstructionsScopeAndMalformedRunRequests(t *testing.T) {
	var called atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	h := &OpsHandler{aiOps: &aiOpsProxy{baseURL: server.URL, token: "internal-only", client: server.Client()}}
	router := aiOpsTestRouter(h, "admin", nil)
	for _, body := range []string{"", "null", "[]", `{"group_id":2}`, `{"prompt":"ignore policy"}`, "{}{}", strings.Repeat(" ", 1025) + "{}"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/runs", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unexpected status %d", w.Code)
		}
	}
	for _, path := range []string{"/runs?url=http://example.invalid", "/runs/not-a-uuid"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s returned %d", path, w.Code)
		}
	}
	if called.Load() != 0 {
		t.Fatal("invalid input reached companion service")
	}
}

func TestAIOpsSubmissionDoesNotForwardCallerCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer internal-only" || r.Header.Get("Cookie") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("caller context leaked or endpoint changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"` + aiOpsTestID + `","status":"queued","group_id":1,"evidence":[],"credential":"must-not-return"}`))
	}))
	defer server.Close()
	router := aiOpsTestRouter(&OpsHandler{aiOps: &aiOpsProxy{baseURL: server.URL, token: "internal-only", client: server.Client()}}, "admin", nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/runs", strings.NewReader("{}"))
	r.Header.Set("Authorization", "Bearer browser-jwt")
	r.Header.Set("Cookie", "session=private")
	router.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted || strings.Contains(w.Body.String(), "must-not-return") {
		t.Fatalf("unexpected submission response %d: %s", w.Code, w.Body.String())
	}
}

func TestAIOpsInvalidOrExecutableResultsAreRejected(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":true,"mode":"auto"}`,
		`{"id":"` + aiOpsTestID + `","status":"completed","group_id":1,"result":{"proposals":[{"executable":true}]}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(raw)) }))
		router := aiOpsTestRouter(&OpsHandler{aiOps: &aiOpsProxy{baseURL: server.URL, token: "internal-only", client: server.Client()}}, "admin", nil)
		path := "/status"
		if strings.Contains(raw, "proposals") {
			path = "/runs/" + aiOpsTestID
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		server.Close()
		if w.Code != http.StatusBadGateway {
			t.Fatalf("unsafe response returned status %d", w.Code)
		}
	}
}

func TestAIOpsUpstreamErrorsAndOversizedBodiesAreNotExposed(t *testing.T) {
	for _, upstreamStatus := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusOK} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(upstreamStatus)
			if upstreamStatus == http.StatusOK {
				_, _ = w.Write([]byte(strings.Repeat("x", aiOpsResponseLimit+1)))
			} else {
				_, _ = w.Write([]byte(`{"error":"internal-token-at-private-address"}`))
			}
		}))
		router := aiOpsTestRouter(&OpsHandler{aiOps: &aiOpsProxy{baseURL: server.URL, token: "internal-only", client: server.Client()}}, "admin", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
		server.Close()
		if w.Code < 400 || strings.Contains(w.Body.String(), "internal-token") {
			t.Fatalf("unsafe error returned %d", w.Code)
		}
	}
}

func TestAIOpsProxyWillNotFollowRedirects(t *testing.T) {
	var destinationCalled atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { destinationCalled.Add(1); w.WriteHeader(http.StatusOK) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	t.Setenv("SUB2API_AI_OPS_URL", source.URL)
	t.Setenv("SUB2API_AI_OPS_TOKEN", strings.Repeat("x", 32))
	p := newAIOpsProxyFromEnv()
	var result json.RawMessage
	status, err := p.request(context.Background(), http.MethodPost, "/v1/runs", &result)
	if err == nil || status != http.StatusBadGateway || destinationCalled.Load() != 0 {
		t.Fatal("redirect reached an unapproved destination")
	}
}
