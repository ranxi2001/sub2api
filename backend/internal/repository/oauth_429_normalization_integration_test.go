//go:build integration

package repository

import (
	"context"
	"encoding/json"
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

func TestOAuth429HTTPForwardNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, input, mapping, wire string
		accepted                   bool
	}{
		{"provider_prefix", "openai/gpt-6-astra", `{}`, "gpt-6-astra", true},
		{"wildcard_to_prefix", "alias-one", `{"alias-*":"openai/gpt-6-astra","gpt-6-luna":"gpt-6-luna"}`, "gpt-6-astra", true},
		{"wrong_target", "alias-one", `{"alias-*":"openai/gpt-6-astra"}`, "gpt-6-luna", false},
		{"illegal_original", "illegal-input", `{"alias-*":"openai/gpt-6-astra"}`, "gpt-6-astra", false},
		{"another_allowed_alias", "alias-other", `{"alias-one":"openai/gpt-6-astra","alias-other":"gpt-6-luna"}`, "gpt-6-astra", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, a, _ := oauth429Fixture(t, nil)
			_, err := integrationDB.ExecContext(t.Context(), `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}',$2::jsonb) WHERE id=$1`, a.ID, tc.mapping)
			require.NoError(t, err)
			runner, calls, _ := oauth429Runner(t, repo, []string{"23", "27"})
			runner.StartOAuth429Worker()
			defer runner.Stop()
			business := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); w.WriteHeader(429) }))
			defer business.Close()
			transport := &accountResponseTransport{base: http.DefaultTransport}
			for i := 0; i < 20; i++ {
				ctx := context.WithValue(t.Context(), ctxkey.ClientRequestID, fmt.Sprintf("normalization-%d-%d", a.ID, i))
				ctx = context.WithValue(ctx, ctxkey.Model, tc.input)
				ctx = requestcapture.WithAccount(ctx, a.ID)
				ctx = service.WithOAuth429WireModel(ctx, tc.wire)
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, business.URL+"/v1/responses", strings.NewReader(`{"model":"`+tc.wire+`","stream":true}`))
				require.NoError(t, err)
				req.GetBody = nil
				resp, err := transport.RoundTrip(req)
				require.NoError(t, err)
				_ = resp.Body.Close()
			}
			if !tc.accepted {
				// Waiting for an accepted sentinel drains this account's FIFO first;
				// the illegal messages cannot hide behind a sleep-based assertion.
				ctx := context.WithValue(t.Context(), ctxkey.ClientRequestID, fmt.Sprintf("normalization-sentinel-%d", a.ID))
				ctx = context.WithValue(ctx, ctxkey.Model, "alias-one")
				ctx = requestcapture.WithAccount(ctx, a.ID)
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, business.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-6-astra"}`))
				require.NoError(t, err)
				resp, err := transport.RoundTrip(req)
				require.NoError(t, err)
				_ = resp.Body.Close()
				require.Eventually(t, func() bool {
					var count int
					return integrationDB.QueryRowContext(t.Context(), `SELECT count(*) FROM oauth_429_request_dedup WHERE account_id=$1`, a.ID).Scan(&count) == nil && count > 0
				}, 3*time.Second, 20*time.Millisecond)
				var data []byte
				require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT data FROM oauth_429_tasks WHERE account_id=$1`, a.ID).Scan(&data))
				var task service.OAuth429Task
				require.NoError(t, json.Unmarshal(data, &task))
				require.Len(t, task.Samples, 1, "an unrelated allowed alias must not authorize illegal input or another wire target")
				require.Zero(t, calls.Load())
				return
			}
			require.Eventually(t, func() bool {
				got, err := repo.GetByID(t.Context(), a.ID)
				return err == nil && got.IsOAuth429ModelBlocked(tc.input)
			}, 8*time.Second, 20*time.Millisecond, "Forward-normalized model must complete both tests and block only its canonical target")
			var data []byte
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT data FROM oauth_429_tasks WHERE account_id=$1 AND model='gpt-6-astra' AND state='complete'`, a.ID).Scan(&data))
			var task service.OAuth429Task
			require.NoError(t, json.Unmarshal(data, &task))
			require.Equal(t, tc.input, task.RequestModel)
			require.Len(t, task.Runs, 2)
			require.EqualValues(t, 2, calls.Load())
			got, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.False(t, got.IsOAuth429ModelBlocked("gpt-6-luna"))
			require.True(t, got.IsModelSupported("gpt-6-luna"))
			blocks := got.Extra[service.OAuth429ModelBlocksKey].(map[string]any)
			require.Len(t, blocks, 1)
			require.NotNil(t, blocks["gpt-6-astra"])
		})
	}
}
