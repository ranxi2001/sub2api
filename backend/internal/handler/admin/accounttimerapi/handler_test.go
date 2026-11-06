package accounttimerapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/accounttimer"
	"github.com/gin-gonic/gin"
)

type fakeRepo struct {
	accounttimer.Repository
	calls              int
	id, actor, version int64
	action             string
	err                error
	window             accounttimer.DailyWindowRequest
}

func (r *fakeRepo) List(_ context.Context, id int64) (accounttimer.Snapshot, error) {
	r.calls++
	r.id = id
	return accounttimer.Snapshot{Tasks: []accounttimer.Task{}}, r.err
}
func (r *fakeRepo) Preview(in accounttimer.Input) (accounttimer.Preview, error) {
	r.calls++
	return accounttimer.Resolve(in, time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), accounttimer.DefaultPolicy())
}
func (r *fakeRepo) Save(ctx context.Context, id, actor int64, req accounttimer.SaveRequest) (accounttimer.Task, error) {
	r.calls++
	r.id = id
	r.actor = actor
	r.action = req.Action
	r.version = req.ExpectedVersion
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded admin transaction")
	}
	return accounttimer.Task{AccountID: id, Action: req.Action}, r.err
}
func (r *fakeRepo) Cancel(_ context.Context, id, actor int64, action string, version int64) error {
	r.calls++
	r.id = id
	r.actor = actor
	r.action = action
	r.version = version
	return r.err
}
func (r *fakeRepo) SaveDailyWindow(ctx context.Context, id, actor int64, req accounttimer.DailyWindowRequest) (accounttimer.Snapshot, error) {
	r.calls++
	r.id, r.actor, r.window = id, actor, req
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded daily window transaction")
	}
	return accounttimer.Snapshot{Tasks: []accounttimer.Task{{AccountID: id, Action: "open", Recurrence: "daily", DailyTime: req.OpenTime}, {AccountID: id, Action: "close", Recurrence: "daily", DailyTime: req.CloseTime}}}, r.err
}
func router(repo *fakeRepo, role string, actor int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &Handler{Repository: func() accounttimer.Repository { return repo }, Identity: func(*gin.Context) (int64, string) { return actor, role }}
	r.GET("/accounts/:id/timers", h.List)
	r.POST("/accounts/:id/timers/preview", h.Preview)
	r.POST("/accounts/:id/timers", h.Save)
	r.POST("/accounts/:id/timers/daily-window", h.SaveDailyWindow)
	r.DELETE("/accounts/:id/timers/:action", h.Cancel)
	return r
}
func TestAllTimerAPIsRejectNonAdminBeforeAccess(t *testing.T) {
	endpoints := []struct{ method, path string }{{"GET", "/accounts/7/timers"}, {"POST", "/accounts/7/timers/preview"}, {"POST", "/accounts/7/timers"}, {"POST", "/accounts/7/timers/daily-window"}, {"DELETE", "/accounts/7/timers/open?version=1"}}
	for _, role := range []string{"", "user", "observer"} {
		for _, ep := range endpoints {
			t.Run(role+ep.method+ep.path, func(t *testing.T) {
				repo := &fakeRepo{}
				w := httptest.NewRecorder()
				router(repo, role, 9).ServeHTTP(w, httptest.NewRequest(ep.method, ep.path, strings.NewReader(`{}`)))
				if w.Code != http.StatusForbidden || repo.calls != 0 {
					t.Fatalf("status %d calls %d", w.Code, repo.calls)
				}
			})
		}
	}
}
func TestTimerAPIPropagatesActorAndVersion(t *testing.T) {
	repo := &fakeRepo{}
	r := router(repo, "admin", 19)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/accounts/7/timers", strings.NewReader(`{"mode":"relative","hours":8,"timezone":"Asia/Shanghai","action":"open","expected_version":3}`)))
	if w.Code != 200 || repo.id != 7 || repo.actor != 19 || repo.version != 3 || repo.action != "open" {
		t.Fatalf("%d %+v", w.Code, repo)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/accounts/7/timers/close?version=4", nil))
	if w.Code != 200 || repo.actor != 19 || repo.version != 4 || repo.action != "close" {
		t.Fatalf("%d %+v", w.Code, repo)
	}
}
func TestTimerAPIRejectsMalformedIDsBodiesAndVersions(t *testing.T) {
	for _, ep := range []struct{ method, path, body string }{{"GET", "/accounts/0/timers", ""}, {"POST", "/accounts/x/timers", "{}"}, {"POST", "/accounts/7/timers", "{"}, {"DELETE", "/accounts/7/timers/open?version=x", ""}} {
		repo := &fakeRepo{}
		w := httptest.NewRecorder()
		router(repo, "admin", 9).ServeHTTP(w, httptest.NewRequest(ep.method, ep.path, strings.NewReader(ep.body)))
		if w.Code != 400 || repo.calls != 0 {
			t.Fatalf("%s status %d calls %d", ep.path, w.Code, repo.calls)
		}
	}
}
func TestTimerAPIErrorsAreStructuredAndRedacted(t *testing.T) {
	for _, code := range []string{"conflict", "version", "account_deleted", "dst"} {
		repo := &fakeRepo{err: &accounttimer.Error{Code: code}}
		w := httptest.NewRecorder()
		router(repo, "admin", 9).ServeHTTP(w, httptest.NewRequest("POST", "/accounts/7/timers", strings.NewReader(`{"action":"open"}`)))
		want := 400
		if code == "conflict" || code == "version" {
			want = 409
		}
		if code == "account_deleted" {
			want = 404
		}
		var body struct{ Reason string }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != want || body.Reason != "TIMER_"+code {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}
func TestPreviewNextBeijingEightReturnsConcreteDateAndOffset(t *testing.T) {
	repo := &fakeRepo{}
	w := httptest.NewRecorder()
	router(repo, "admin", 9).ServeHTTP(w, httptest.NewRequest("POST", "/accounts/7/timers/preview", strings.NewReader(`{"mode":"absolute","time":"08:00","timezone":"Asia/Shanghai"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"local_date":"2026-10-23"`) || !strings.Contains(w.Body.String(), `"offset":"+08:00"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestDailyWindowAPIPropagatesBothVersionsAndReturnsPair(t *testing.T) {
	repo := &fakeRepo{}
	w := httptest.NewRecorder()
	router(repo, "admin", 19).ServeHTTP(w, httptest.NewRequest("POST", "/accounts/7/timers/daily-window", strings.NewReader(`{"open_time":"08:00","close_time":"01:00","timezone":"Asia/Shanghai","expected_open_version":3,"expected_close_version":5}`)))
	if w.Code != 200 || repo.calls != 1 || repo.id != 7 || repo.actor != 19 || repo.window.ExpectedOpenVersion != 3 || repo.window.ExpectedCloseVersion != 5 || repo.window.OpenTime != "08:00" || repo.window.CloseTime != "01:00" || repo.window.Timezone != "Asia/Shanghai" {
		t.Fatalf("%d %+v %s", w.Code, repo, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"recurrence":"daily"`) || !strings.Contains(w.Body.String(), `"daily_time":"01:00"`) {
		t.Fatalf("pair snapshot missing: %s", w.Body.String())
	}
}

func TestDailyWindowAPIRejectsMalformedBodyAndPreservesVersionConflict(t *testing.T) {
	repo := &fakeRepo{}
	w := httptest.NewRecorder()
	router(repo, "admin", 19).ServeHTTP(w, httptest.NewRequest("POST", "/accounts/7/timers/daily-window", strings.NewReader(`{`)))
	if w.Code != 400 || repo.calls != 0 {
		t.Fatalf("malformed body status %d calls %d", w.Code, repo.calls)
	}
	repo.err = &accounttimer.Error{Code: "version"}
	w = httptest.NewRecorder()
	router(repo, "admin", 19).ServeHTTP(w, httptest.NewRequest("POST", "/accounts/7/timers/daily-window", strings.NewReader(`{"open_time":"08:00","close_time":"01:00","timezone":"Asia/Shanghai"}`)))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "TIMER_version") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
