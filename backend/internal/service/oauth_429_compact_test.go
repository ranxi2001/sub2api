//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestOAuth429CompactGlobalForwardAndAdmission(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, accountRule := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "normal", true: "passthrough"}[passthrough], map[bool]string{false: "global", true: "account_precedence"}[accountRule]}, "/"), func(t *testing.T) {
				s := newAstraOAuthSetup(t, passthrough)
				s.svc.cfg.Gateway.OpenAICompactModel = "gpt-6-luna"
				s.account.Credentials["model_mapping"] = map[string]any{"alias-one": "gpt-6-astra", "other-alias": "gpt-6-luna"}
				target := "gpt-6-luna"
				if accountRule {
					s.account.Credentials["compact_model_mapping"] = map[string]any{"alias-one": "gpt-6-astra"}
					target = "gpt-6-astra"
				}
				ctx := context.WithValue(t.Context(), ctxkey.Model, "alias-one")
				ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "compact-original")
				s.c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil).WithContext(ctx)
				s.upstream.resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"compact","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))}
				_, err := s.svc.Forward(ctx, s.c, s.account, astraRequestBody("alias-one", false, "", ""))
				require.NoError(t, err)
				require.Equal(t, target, s.upstream.lastReq.Context().Value(oauth429WireModelKey{}))
				require.Equal(t, "gpt-6-luna", s.upstream.lastReq.Context().Value(oauth429CompactModelKey{}))
				require.Equal(t, target, ResolveOAuth429TargetModel(s.account, "alias-one", "/responses/compact", "gpt-6-luna"))
				require.True(t, OAuth429RequestModelSupported(s.account, "alias-one", "/responses/compact", "gpt-6-luna"))
				// Passthrough's normal route retains the public input; this remains
				// a different wire model from the compact override.
				normal := ResolveOAuth429TargetModel(s.account, "alias-one", "/responses", "gpt-6-luna")
				if normal != target {
					s.account.Extra[OAuth429ModelBlocksKey] = map[string]any{target: map[string]any{}}
					require.True(t, OAuth429RequestModelSupported(s.account, "alias-one", "/responses", "gpt-6-luna"))
					require.False(t, OAuth429RequestModelSupported(s.account, "alias-one", "/responses/compact", "gpt-6-luna"))
					require.True(t, s.svc.isOpenAIAccountRequestRuntimeBlocked(s.account, "alias-one", true))
					require.False(t, s.svc.isOpenAIAccountRequestRuntimeBlocked(s.account, "alias-one", false))
					s.account.Extra[OAuth429ModelBlocksKey] = map[string]any{normal: map[string]any{}}
					require.True(t, OAuth429RequestModelSupported(s.account, "alias-one", "/responses/compact", "gpt-6-luna"))
					require.False(t, OAuth429RequestModelSupported(s.account, "alias-one", "/responses", "gpt-6-luna"))
					require.False(t, s.svc.isOpenAIAccountRequestRuntimeBlocked(s.account, "alias-one", true))
				}
				// Passthrough defaults may intentionally bypass the ordinary mapping
				// allowlist, so enable its existing strict filter for this negative.
				s.account.Extra["openai_passthrough_model_filter"] = true
				require.False(t, OAuth429RequestModelSupported(s.account, "illegal-input", "/responses/compact", "gpt-6-luna"))
			})
		}
	}
}
