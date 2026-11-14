//go:build integration

package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMatureProvidersRefreshClaimAndLowBalance(t *testing.T) {
	db := matureProviderDatabase(t)
	cfg := &config.Config{}
	cfg.Totp.EncryptionKey = strings.Repeat("56", 32)
	crypt, err := repository.NewAESEncryptor(cfg)
	require.NoError(t, err)
	var requests atomic.Int32
	client := &http.Client{Transport: matureProviderTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		raw := `{"code":0,"data":{"balance":2}}`
		if strings.Contains(r.URL.Path, "stats") {
			raw = `{"code":0,"data":{}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
	})}
	newHandler := func() *admin.ChannelHandler {
		h := admin.NewChannelHandler(service.NewChannelService(repository.NewChannelRepository(db), nil, nil, nil, nil), nil, nil)
		if dep, ok := any(h).(interface {
			SetMatureProviderDependencies(service.SecretEncryptor, *http.Client)
		}); ok {
			dep.SetMatureProviderDependencies(crypt, client)
		}
		return h
	}
	a, b := newHandler(), newHandler()
	r := gin.New()
	registerChannelRoutes(r.Group("/api/v1/admin"), &handler.Handlers{Admin: &handler.AdminHandlers{Channel: a}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/admin/providers", strings.NewReader(`{"name":"Refresh fixture","site_type":"sub2api","base_url":"https://provider.example.invalid","auth_mode":"token","access_token":"synthetic","refresh_interval_seconds":60,"low_balance_threshold":5}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, "explicit provider refresh policy must persist without starting network work: %s", w.Body.String())
	require.Zero(t, requests.Load())
	var p struct{ Data struct{ ID int64 } }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
	var wg sync.WaitGroup
	for _, h := range []*admin.ChannelHandler{a, b} {
		wg.Add(1)
		go func(h *admin.ChannelHandler) {
			defer wg.Done()
			worker, ok := any(h).(interface {
				RefreshDueMatureProvider(context.Context) (bool, error)
			})
			if !ok {
				t.Error("provider refresh worker is unavailable")
				return
			}
			_, e := worker.RefreshDueMatureProvider(context.Background())
			if e != nil {
				t.Error(e)
			}
		}(h)
	}
	wg.Wait()
	require.EqualValues(t, 2, requests.Load(), "only one instance may refresh the due provider")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM mature_provider_alerts WHERE provider_id=$1 AND resolved_at IS NULL`, p.Data.ID).Scan(&count))
	require.Equal(t, 1, count)
	_, err = db.Exec(`UPDATE mature_providers SET next_refresh_at=NOW() WHERE id=$1`, p.Data.ID)
	require.NoError(t, err)
	worker := any(a).(interface {
		RefreshDueMatureProvider(context.Context) (bool, error)
	})
	_, err = worker.RefreshDueMatureProvider(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM mature_provider_alerts WHERE provider_id=$1`, p.Data.ID).Scan(&count))
	require.Equal(t, 1, count, "repeated low samples must not create duplicate alert episodes")
	var revision int64
	require.NoError(t, db.QueryRow(`SELECT revision FROM mature_providers WHERE id=$1`, p.Data.ID).Scan(&revision))
	w = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", fmt.Sprintf("/api/v1/admin/providers/%d", p.Data.ID), strings.NewReader(fmt.Sprintf(`{"name":"Changed identity","site_type":"sub2api","base_url":"https://other.example.invalid","revision":%d}`, revision)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM mature_provider_alerts WHERE provider_id=$1 AND resolved_at IS NULL`, p.Data.ID).Scan(&count))
	require.Zero(t, count, "identity change must end the previous balance episode")
}
