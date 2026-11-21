//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise Forward's real body rewrite and real HTTP, rather than filling the
// expected canonical model into a synthetic builder request.
type oauth429ForwardHTTP struct {
	target *url.URL
	seen   *http.Request
}

func (u *oauth429ForwardHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.seen = req
	next := req.Clone(req.Context())
	target := *u.target
	target.Path = req.URL.Path
	next.URL, next.Host = &target, ""
	return http.DefaultClient.Do(next)
}
func (u *oauth429ForwardHTTP) DoWithTLS(req *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, id, n)
}

func TestOAuth429RealForwardNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, input, wire string
		mapping           map[string]any
		passthrough       bool
	}{
		{"prefix", "openai/gpt-6-astra", "gpt-6-astra", nil, false},
		{"wildcard_prefix", "alias-one", "gpt-6-astra", map[string]any{"alias-*": "openai/gpt-6-astra"}, false},
		{"passthrough_retains_prefix", "openai/gpt-6-astra", "openai/gpt-6-astra", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAstraOAuthSetup(t, tc.passthrough)
			if tc.mapping != nil {
				s.account.Credentials["model_mapping"] = tc.mapping
			}
			wires := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				wires <- gjson.GetBytes(body, "model").String()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, codexCompletedSSE(`{"id":"fixture","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			upstream := &oauth429ForwardHTTP{target: target}
			s.svc.httpUpstream = upstream
			ctx := context.WithValue(t.Context(), ctxkey.Model, tc.input)
			ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "forward-normalization")
			s.c.Request = s.c.Request.WithContext(ctx)
			require.True(t, s.account.IsModelSupported(tc.input))
			_, err = s.svc.Forward(ctx, s.c, s.account, astraRequestBody(tc.input, true, "", ""))
			require.NoError(t, err)
			wire := <-wires
			require.Equal(t, tc.wire, wire)
			require.NotNil(t, upstream.seen)
			require.Equal(t, wire, ResolveOAuth429TargetModel(s.account, tc.input, upstream.seen.URL.Path))
			require.Equal(t, wire, upstream.seen.Context().Value(oauth429WireModelKey{}))
			require.Equal(t, tc.input, upstream.seen.Context().Value(ctxkey.Model))
			require.Equal(t, "forward-normalization", upstream.seen.Context().Value(ctxkey.ClientRequestID))
		})
	}
}

func TestOAuth429EndpointResolution(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"model_mapping":         map[string]any{"alias-*": "openai/gpt-6-astra"},
		"compact_model_mapping": map[string]any{"alias-one": "gpt-6-luna"},
	}}
	require.Equal(t, "gpt-6-astra", ResolveOAuth429TargetModel(a, "alias-one", "/v1/responses"))
	require.Equal(t, "gpt-6-luna", ResolveOAuth429TargetModel(a, "alias-one", "/v1/responses/compact"))
	require.False(t, a.IsModelSupported("illegal-input"))
	a.Extra = map[string]any{"openai_passthrough": true}
	require.Equal(t, "alias-one", ResolveOAuth429TargetModel(a, "alias-one", "/v1/responses"))
	require.Equal(t, "gpt-6-luna", ResolveOAuth429TargetModel(a, "alias-one", "/v1/responses/compact"))
	// A canonical block must follow every alias to this target and no alias to
	// another model. The public mapping still controls admission independently.
	a.Extra = map[string]any{OAuth429ModelBlocksKey: map[string]any{"gpt-6-astra": map[string]any{}}}
	a.Credentials["model_mapping"] = map[string]any{"alias-*": "openai/gpt-6-astra", "other": "gpt-6-luna"}
	require.True(t, a.IsOAuth429ModelBlocked("alias-one"))
	require.True(t, a.IsOAuth429ModelBlocked("alias-two"))
	require.False(t, a.IsOAuth429ModelBlocked("other"))
	require.False(t, OAuth429RequestModelSupported(a, "alias-one", "/v1/responses"))
	require.True(t, OAuth429RequestModelSupported(a, "alias-one", "/v1/responses/compact"), "a normal target block must not block a different compact target")
	a.Extra[OAuth429ModelBlocksKey] = map[string]any{"gpt-6-luna": map[string]any{}}
	require.True(t, OAuth429RequestModelSupported(a, "alias-one", "/v1/responses"))
	require.False(t, OAuth429RequestModelSupported(a, "alias-one", "/v1/responses/compact"))
	require.False(t, strings.Contains(ResolveOAuth429TargetModel(a, "alias-one", ""), "openai/"))
}
