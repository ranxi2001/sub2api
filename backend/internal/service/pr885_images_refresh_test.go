//go:build bps_request_integration

package service

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/stretchr/testify/require"
)

func TestExcelBPSImagesRequestRefreshAndFallbackShareExit(t *testing.T) {
	observed := make(chan requestProxyObservation, 4)
	gateway, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		observed <- requestProxyObservation{id: id, path: r.URL.Path}
		if r.URL.Path == "/oauth/token" {
			_, _ = io.WriteString(w, "{}")
			return
		}
		if r.URL.Host == "bps.openai.com" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"detail":"format rejected"}`)
			return
		}
		response := openAIImagesJSONResponse()
		defer response.Body.Close()
		for k, v := range response.Header {
			w.Header()[k] = v
		}
		_, _ = io.Copy(w, response.Body)
	})
	reauth, reader, repo, _, _ := newExcelReauthTestService(t)
	account.ID = reader.account.ID
	account.Credentials = reader.account.Credentials
	account.Extra["openai_excel_bps_models"] = []any{"gpt-image-2"}
	account.Extra[OpenAIProxyModeKey] = OpenAIProxyModeRequest
	account.Extra[OpenAIProxyIDsKey] = []int64{1, 2, 3}
	reader.account = account
	reauth.oauth.proxyRepo = gateway.proxyRepo
	gateway.excelOAuthReauth = reauth
	storeExcelTestCredentials(t, reauth, repo, excelTestCredentials("stale", time.Now().Add(-time.Hour)))
	reauth.oauth.oauthClient = &independentExcelProxyClient{&excelRefreshTestClient{credentials: excelTestCredentials("fresh", time.Now().Add(time.Hour))}}
	busy, err := mihomo.AcquireBPSStaticRequestLease(t.Context(), account.ID, []int64{1})
	require.NoError(t, err)
	defer busy.Release()
	body := []byte(`{"model":"gpt-image-2","prompt":"local image fixture"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	parsed, err := gateway.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	_, err = gateway.ForwardImages(ctx, c, account, body, parsed, "")
	require.NoError(t, err)
	seen := make([]requestProxyObservation, 0, 3)
	for range 3 {
		select {
		case item := <-observed:
			seen = append(seen, item)
		case <-ctx.Done():
			t.Fatal("missing refresh/BPS/fallback request")
		}
	}
	require.Equal(t, "/oauth/token", seen[0].path)
	require.Equal(t, "/basispoints/api/images/generations", seen[1].path)
	require.Equal(t, "/backend-api/codex/images/generations", seen[2].path)
	require.NotEqual(t, 1, seen[0].id, "busy proxy must not win")
	require.Equal(t, seen[0].id, seen[1].id)
	require.Equal(t, seen[1].id, seen[2].id)
	t.Logf("observed refresh_proxy=%d bps_images_proxy=%d fallback_proxy=%d", seen[0].id, seen[1].id, seen[2].id)
}
