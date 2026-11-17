package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProviderProfitBackgroundJobSurvivesDisconnect(t *testing.T) {
	store := &profitSyncStore{p: service.MatureProvider{ID: 5, Revision: 7, BaseURL: "https://wallet.invalid", SiteType: "sub2api", AuthMode: "token"}}
	h := NewChannelHandler(service.NewChannelService(store, nil, nil, nil, nil), nil, nil)
	t.Cleanup(h.StopMatureProviderRefresh)
	reading, release := make(chan struct{}), make(chan struct{})
	h.SetMatureProviderDependencies(profitFakeCrypt{}, &http.Client{Transport: profitTransport(func(r *http.Request) (*http.Response, error) {
		var data any
		switch r.URL.Path {
		case "/api/v1/auth/me":
			close(reading)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			data = map[string]any{"id": 7, "run_mode": "standard"}
		case "/api/v1/usage":
			size, err := strconv.Atoi(r.URL.Query().Get("page_size"))
			if err != nil {
				return nil, err
			}
			data = profitPageSized(1, size, 0, []map[string]any{})
		default:
			return nil, fmt.Errorf("unexpected fixture route")
		}
		raw, err := json.Marshal(map[string]any{"code": 0, "data": data})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})})
	router := gin.New()
	const route = "/admin/providers/:id/profit-charges/sync"
	const base = "/admin/providers/5/profit-charges/sync"
	router.POST(route, h.SyncProviderProfitCharges)
	router.GET(route+"/:job", h.ProviderProfitSyncStatus)
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := call(ctx, "POST", base+"?background=true", profitWindowBody)
	require.Equal(t, 200, start.Code, start.Body.String())
	var handle struct {
		Data struct {
			ID string `json:"job_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(start.Body.Bytes(), &handle))
	require.NotEmpty(t, handle.Data.ID)
	select {
	case <-reading:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not enter upstream read")
	}
	cancel()
	bg := context.Background()
	again := call(bg, "POST", base+"?background=true", profitWindowBody)
	require.JSONEq(t, start.Body.String(), again.Body.String())
	other := call(bg, "POST", base+"?background=true", strings.Replace(profitWindowBody, "00:05:00Z", "00:06:00Z", 1))
	require.Equal(t, 429, other.Code)
	statusPath := base + "/" + handle.Data.ID
	running := call(bg, "GET", statusPath, "")
	require.Equal(t, 200, running.Code)
	require.Contains(t, running.Body.String(), `"status":"running"`)
	require.Equal(t, "no-store", running.Header().Get("Cache-Control"))
	foreign := call(bg, "GET", strings.Replace(statusPath, "/5/", "/6/", 1), "")
	require.Equal(t, 404, foreign.Code)
	close(release)
	require.Eventually(t, func() bool {
		return strings.Contains(call(bg, "GET", statusPath, "").Body.String(), `"status":"succeeded"`)
	}, 5*time.Second, 5*time.Millisecond)
	result := call(bg, "GET", statusPath, "")
	require.JSONEq(t, `{"code":0,"message":"success","data":{"status":"succeeded","matched":0,"unmatched":3}}`, result.Body.String())
	require.Equal(t, 1, store.writes)
}

// A scheduled metrics refresh races the first admission. Retrying before any
// page was consumed is safe; subsequent batches must not repeat password login.
// A real identity change instead of a metrics revision must stop the scan.
type profitRacingStore struct {
	*profitSyncStore
	attempts       int
	changeIdentity bool
}

func (s *profitRacingStore) WithMatureProviderSession(ctx context.Context, id, rev int64, run func(context.Context, *service.MatureProvider, string) (service.ProviderSessionUpdate, error)) (*service.MatureProvider, error) {
	s.attempts++
	if s.attempts == 1 {
		s.p.Revision++
		if s.changeIdentity {
			s.p.Account = "other@fixture.invalid"
		}
		return nil, service.ErrMatureProviderConflict
	}
	return s.profitSyncStore.WithMatureProviderSession(ctx, id, rev, run)
}
func TestProviderProfitBatchesRecheckRevisionAndReuseLogin(t *testing.T) {
	for _, change := range []bool{false, true} {
		s := &profitRacingStore{profitSyncStore: &profitSyncStore{p: service.MatureProvider{ID: 5, Revision: 7, BaseURL: "https://wallet.invalid", SiteType: "sub2api", AuthMode: "password", Account: "bill@fixture.invalid"}}, changeIdentity: change}
		h := NewChannelHandler(service.NewChannelService(s, nil, nil, nil, nil), nil, nil)
		logins := 0
		var bill profitLiveBill
		h.SetMatureProviderDependencies(profitFakeCrypt{}, &http.Client{Transport: profitTransport(func(r *http.Request) (*http.Response, error) {
			var data any
			if r.URL.Path == "/api/v1/auth/login" {
				logins++
				data = map[string]any{"access_token": "fixture-password-session", "token_type": "Bearer"}
			} else {
				require.Equal(t, "Bearer fixture-password-session", r.Header.Get("Authorization"))
				_, data = bill.serve(t, r)
			}
			raw, err := json.Marshal(map[string]any{"code": 0, "data": data})
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}, nil
		})})
		w := service.ProviderProfitWindow{From: profitInstant, Until: profitInstant.Add(15 * time.Minute)}
		epoch, err := s.ProviderProfitIdentityFrom(context.Background(), s.p.ID)
		require.NoError(t, err)
		result := h.runProfitSync(context.Background(), s.p, epoch, w, s)
		if change {
			require.Equal(t, "conflict", result.Reason)
			require.Zero(t, logins)
			require.Zero(t, s.writes)
			continue
		}
		require.Equal(t, "succeeded", result.Status)
		require.Greater(t, s.sessions, 1)
		require.Equal(t, 1, logins)
		require.Equal(t, 1, s.writes)
	}
}

func TestProviderProfitJobsStopAndRedact(t *testing.T) {
	var q providerProfitJobs
	w := service.ProviderProfitWindow{From: profitInstant, Until: profitInstant.Add(time.Minute)}
	t.Cleanup(q.stop)
	panicJob, err := q.start(1, w, func(context.Context) profitJobStatus { panic("private-remote-fixture") })
	require.NoError(t, err)
	<-panicJob.done
	result, ok := q.get(1, panicJob.id)
	require.True(t, ok)
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "internal", result.Reason)
	run := func(ctx context.Context) profitJobStatus {
		<-ctx.Done()
		return profitJobStatus{Status: "failed", Reason: "timeout"}
	}
	first, err := q.start(1, w, run)
	require.NoError(t, err)
	second, err := q.start(2, w, run)
	require.NoError(t, err)
	_, err = q.start(3, w, run)
	require.ErrorIs(t, err, errProfitJobsBusy)
	q.stop()
	for _, j := range []*profitJob{first, second} {
		s, ok := q.get(j.provider, j.id)
		require.True(t, ok)
		require.Equal(t, "timeout", s.Reason)
	}
	_, err = q.start(1, w, run)
	require.ErrorIs(t, err, errProfitJobsBusy)
}
