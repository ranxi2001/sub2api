//go:build integration

package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuth429HTTPParsedModelSurvivesStreamingAndLargeBody(t *testing.T) {
	for _, tc := range []struct {
		name             string
		noGetBody, large bool
	}{{"stream_without_getbody", true, false}, {"model_after_large_input", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			repo, a, _ := oauth429Fixture(t, nil)
			_, err := integrationDB.ExecContext(t.Context(), `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}','{"alias-*":"gpt-6-astra","gpt-6-luna":"gpt-6-luna"}') WHERE id=$1`, a.ID)
			require.NoError(t, err)
			runner, calls, _ := oauth429Runner(t, repo, []string{"429", "429", "429", "429", "429"})
			runner.StartOAuth429Worker()
			defer runner.Stop()
			business := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); w.WriteHeader(429) }))
			defer business.Close()
			transport := &accountResponseTransport{base: http.DefaultTransport}
			for i := 0; i < 20; i++ {
				ctx := context.WithValue(t.Context(), ctxkey.ClientRequestID, fmt.Sprintf("metadata-%d-%d", a.ID, i))
				// Same parsed metadata set by setOpsRequestContext before all HTTP dispatch.
				ctx = context.WithValue(ctx, ctxkey.Model, "alias-one")
				ctx = requestcapture.WithAccount(ctx, a.ID)
				input := "fixture"
				if tc.large {
					input = strings.Repeat("x", (1<<20)+128)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, business.URL+"/v1/responses", strings.NewReader(`{"input":"`+input+`","stream":true,"model":"gpt-6-astra"}`))
				require.NoError(t, err)
				if tc.noGetBody {
					req = req.WithContext(service.WithOAuth429WireModel(req.Context(), "gpt-6-astra"))
					req.GetBody = nil
				}
				resp, err := transport.RoundTrip(req)
				require.NoError(t, err)
				_ = resp.Body.Close()
			}
			require.Eventually(t, func() bool {
				got, e := repo.GetByID(t.Context(), a.ID)
				return e == nil && got.HasOAuthDegradationAction()
			}, 8*time.Second, 20*time.Millisecond, "real input alias and wire target must survive streaming or large provider bodies")
			require.EqualValues(t, 5, calls.Load())
			got, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.True(t, got.IsModelSupported("gpt-6-luna"))
		})
	}
}

func TestOAuth429ActualAliasCannotAuthorizeAnotherWireModel(t *testing.T) {
	repo, a, c := oauth429Fixture(t, nil)
	_, err := integrationDB.ExecContext(t.Context(), `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}','{"alias-*":"gpt-6-astra"}') WHERE id=$1`, a.ID)
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		v := oauth429ObservationFixture(a.ID, "gpt-6-luna", c.Revision, i, 429)
		v.RequestModel = "alias-one"
		queued, err := repo.ObserveOAuth429(t.Context(), v)
		require.NoError(t, err)
		require.False(t, queued)
	}
	var count int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT count(*) FROM oauth_429_tasks WHERE account_id=$1`, a.ID).Scan(&count))
	require.Zero(t, count)
}

type oauth429TransportFailure struct{}

func (oauth429TransportFailure) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("fixture transport failed before headers")
}
func TestOAuth429HTTPWithoutResponseDoesNotSample(t *testing.T) {
	_, a, _ := oauth429Fixture(t, nil)
	transport := &accountResponseTransport{base: oauth429TransportFailure{}}
	for i := 0; i < 20; i++ {
		ctx := context.WithValue(t.Context(), ctxkey.Model, "gpt-6-astra")
		ctx = context.WithValue(ctx, ctxkey.ClientRequestID, fmt.Sprintf("no-response-%d", i))
		ctx = requestcapture.WithAccount(ctx, a.ID)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1/v1/responses", strings.NewReader(`{"model":"gpt-6-astra"}`))
		require.NoError(t, err)
		response, err := transport.RoundTrip(req)
		require.Error(t, err)
		require.Nil(t, response)
	}
	var count int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT count(*) FROM oauth_429_tasks WHERE account_id=$1`, a.ID).Scan(&count))
	require.Zero(t, count)
}
