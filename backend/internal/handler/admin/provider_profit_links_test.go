package admin

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type providerProfitLinkStub struct {
	service.ChannelRepository
	item   service.ProviderProfitLink
	fail   error
	writes int
}

func (s *providerProfitLinkStub) ListProviderProfitLinks(_ context.Context, page, size int) (*service.ProviderProfitLinkPage, error) {
	return &service.ProviderProfitLinkPage{Items: []service.ProviderProfitLink{s.item}, Total: 1, Page: page, PageSize: size}, s.fail
}
func (s *providerProfitLinkStub) SetProviderProfitLink(_ context.Context, id int64, m service.ProviderProfitLinkMutation) (*service.ProviderProfitLink, error) {
	s.writes++
	if id != s.item.ProviderID || m.Revision != s.item.Revision {
		return nil, service.ErrProviderProfitLinkConflict
	}
	s.item.ChannelID = m.ChannelID
	s.item.Revision++
	return &s.item, nil
}
func TestProviderProfitLinkHandlerContract(t *testing.T) {
	s := &providerProfitLinkStub{item: service.ProviderProfitLink{ProviderID: 7, ProviderName: "Fixture", SiteType: "newapi", ChannelID: 19, ChannelName: "Fixture channel", Revision: 3, Status: "disconnected"}}
	h := NewChannelHandler(service.NewChannelService(s, nil, nil, nil, nil), nil, nil)
	r := gin.New()
	r.GET("/providers", h.ListProviderProfitLinks)
	r.PUT("/providers/:id", h.SetProviderProfitLink)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	w := call("GET", "/providers?page=1&page_size=10", "")
	require.Equal(t, 200, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.JSONEq(t, `{"code":0,"message":"success","data":{"items":[{"provider_id":7,"provider_name":"Fixture","site_type":"newapi","channel_id":19,"channel_name":"Fixture channel","revision":3,"observed_at":null,"status":"disconnected","metrics":{"today_spend":null,"total_spend":null,"currency":""}}],"total":1,"page":1,"page_size":10}}`, w.Body.String())
	for _, path := range []string{"/providers?page=0", "/providers?page_size=101", "/providers?page=1.5"} {
		require.Equal(t, 400, call("GET", path, "").Code)
	}
	for _, body := range []string{`{}`, `{"channel_id":5,"revision":3}`, `{"channel_id":0,"revision":3,"confirm":true}`, `{"channel_id":5,"revision":0,"confirm":true}`, `{"channel_id":5,"revision":3,"confirm":true,"unexpected":42}`, `{"channel_id":5,"revision":3,"confirm":true}{}`} {
		require.Equal(t, 400, call("PUT", "/providers/7", body).Code)
	}
	require.Zero(t, s.writes)
	require.Equal(t, 200, call("PUT", "/providers/7", `{"channel_id":5,"revision":3,"confirm":true}`).Code)
	require.Equal(t, 409, call("PUT", "/providers/7", `{"channel_id":19,"revision":3,"confirm":true}`).Code)
	s.fail = errors.New("sensitive fixture driver diagnostic")
	w = call("GET", "/providers", "")
	require.Equal(t, 500, w.Code)
	require.NotContains(t, w.Body.String(), "sensitive fixture")
}
