package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type deepSeekRoundTripFunc func(*http.Request) (*http.Response, error)

func (f deepSeekRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func deepSeekTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestDeepSeekProvider_Search_ProtocolAndMapping(t *testing.T) {
	query := `Unicode 搜索🙂 site:example.com -site:blocked.example "exact phrase"`
	client := &http.Client{Transport: deepSeekRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, deepSeekSearchEndpoint, r.URL.String())
		require.Equal(t, "Bearer fictional-key", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "application/json", r.Header.Get("Accept"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, map[string]any{"query": query, "count": float64(3), "need_content": false}, payload)
		return deepSeekTestResponse(200, `{"results":[{"title":"Example","url":"https://example.com","summary":"Long summary","snippet":"Short snippet","content":"Do not use page body","date":"2026-11-07T00:00:00Z","extra":true},{"url":"https://example.com/2","summary":"","snippet":"Fallback snippet","date":null},{"url":"https://example.com/3","summary":"  ","snippet":"Whitespace fallback"}]}`), nil
	})}
	p := NewDeepSeekProvider("fictional-key", client)
	require.Equal(t, ProviderTypeDeepSeek, p.Name())
	resp, err := p.Search(context.Background(), SearchRequest{Query: query, MaxResults: 3})
	require.NoError(t, err)
	require.Equal(t, query, resp.Query)
	require.Equal(t, []SearchResult{
		{URL: "https://example.com", Title: "Example", Snippet: "Long summary", PageAge: "2026-11-07T00:00:00Z"},
		{URL: "https://example.com/2", Snippet: "Fallback snippet"},
		{URL: "https://example.com/3", Snippet: "Whitespace fallback"},
	}, resp.Results)
	require.NotNil(t, NewDeepSeekProvider("fictional-key", nil).httpClient)
}

func TestDeepSeekProvider_Search_CountAndUnicodeBoundaries(t *testing.T) {
	for _, tc := range []struct{ input, want int }{{-1, 5}, {0, 5}, {1, 1}, {50, 50}, {51, 50}} {
		t.Run(strconv.Itoa(tc.input), func(t *testing.T) {
			client := &http.Client{Transport: deepSeekRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				var payload deepSeekRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				require.Equal(t, strings.Repeat("🙂", 128), payload.Query)
				require.Equal(t, tc.want, payload.Count)
				return deepSeekTestResponse(200, `{"results":[]}`), nil
			})}
			resp, err := NewDeepSeekProvider("fictional-key", client).Search(context.Background(), SearchRequest{Query: strings.Repeat("🙂", 128), MaxResults: tc.input})
			require.NoError(t, err)
			require.NotNil(t, resp.Results)
		})
	}
}

func TestDeepSeekProvider_Search_RejectsInvalidQueryWithoutRequest(t *testing.T) {
	client := &http.Client{Transport: deepSeekRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid query must not reach upstream")
		return nil, nil
	})}
	for _, query := range []string{"", " \t\n", "\u3000", strings.Repeat("界", 129), "\xff"} {
		_, err := NewDeepSeekProvider("fictional-key", client).Search(context.Background(), SearchRequest{Query: query})
		require.ErrorContains(t, err, "1 to 128 Unicode characters")
	}
}

func TestDeepSeekProvider_Search_ResponseErrorsDoNotEchoSecrets(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"unauthorized", 401, `fictional-key`, "status 401"},
		{"rate limited", 429, `{"message":"fictional-key"}`, "status 429"},
		{"server error", 503, `fictional-key`, "status 503"},
		{"invalid JSON", 200, `fictional-key`, "decode response"},
		{"invalid schema", 200, `{"results":"fictional-key"}`, "decode response"},
		{"oversized", 200, strings.Repeat("x", maxResponseSize+1), "size limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: deepSeekRoundTripFunc(func(*http.Request) (*http.Response, error) { return deepSeekTestResponse(tc.status, tc.body), nil })}
			_, err := NewDeepSeekProvider("fictional-key", client).Search(context.Background(), SearchRequest{Query: "test"})
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "fictional-key")
		})
	}
}

func TestDeepSeekProvider_Search_Cancellation(t *testing.T) {
	client := &http.Client{Transport: deepSeekRoundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewDeepSeekProvider("fictional-key", client).Search(ctx, SearchRequest{Query: "test"})
	require.ErrorIs(t, err, context.Canceled)
}

func TestManager_DeepSeek_ProxySelectionAndFallback(t *testing.T) {
	m := NewManager([]ProviderConfig{{Type: ProviderTypeDeepSeek, APIKey: "fictional-key", ProxyURL: "http://provider.example:8080"}, {Type: ProviderTypeTavily, APIKey: "fictional-tavily"}}, nil)
	var requests []string
	client := &http.Client{Transport: deepSeekRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.Host)
		if r.URL.Host == "api.deepseek.com" {
			return deepSeekTestResponse(429, "not exposed"), nil
		}
		return deepSeekTestResponse(200, `{"results":[{"url":"https://example.com","content":"fallback"}]}`), nil
	})}
	m.clientCache["http://provider.example:8080"] = client
	m.clientCache[""] = client
	resp, name, err := m.SearchWithBestProvider(context.Background(), SearchRequest{Query: "query"})
	require.NoError(t, err)
	require.Equal(t, ProviderTypeTavily, name)
	require.Equal(t, []string{"api.deepseek.com", "api.tavily.com"}, requests)
	require.Equal(t, "fallback", resp.Results[0].Snippet)
	// Account proxy takes precedence over the provider proxy for DeepSeek too.
	m.configs = m.configs[:1]
	m.clientCache["http://account.example:8080"] = &http.Client{Transport: deepSeekRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "api.deepseek.com", r.URL.Host)
		return deepSeekTestResponse(200, `{"results":[]}`), nil
	})}
	_, name, err = m.TestSearch(context.Background(), SearchRequest{Query: "query", ProxyURL: "http://account.example:8080"})
	require.NoError(t, err)
	require.Equal(t, ProviderTypeDeepSeek, name)
	require.IsType(t, &DeepSeekProvider{}, m.buildProvider(m.configs[0], client))
}

func TestDeepSeekProvider_Search_TransportError(t *testing.T) {
	failure := errors.New("connection failed")
	client := &http.Client{Transport: deepSeekRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, failure })}
	_, err := NewDeepSeekProvider("fictional-key", client).Search(context.Background(), SearchRequest{Query: "test"})
	require.ErrorIs(t, err, failure)
}

func TestDeepSeekProvider_Search_DoesNotFollowRedirectsOrMutateSharedClient(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: deepSeekRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, deepSeekSearchEndpoint, r.URL.String())
		resp := deepSeekTestResponse(307, "fictional-key")
		resp.Header.Set("Location", "https://other.deepseek.com/collect")
		return resp, nil
	})}
	_, err := NewDeepSeekProvider("fictional-key", client).Search(context.Background(), SearchRequest{Query: "private query"})
	require.ErrorContains(t, err, "status 307")
	require.Equal(t, 1, calls)
	require.Nil(t, client.CheckRedirect)
}

func TestManager_DeepSeek_QuotaSuccessExhaustionAndFailureRollback(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	m := NewManager([]ProviderConfig{{Type: ProviderTypeDeepSeek, APIKey: "fictional-key", QuotaLimit: 1}}, redisClient)
	calls, status := 0, 503
	m.clientCache[""] = &http.Client{Transport: deepSeekRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return deepSeekTestResponse(status, `{"results":[]}`), nil
	})}
	ctx := context.Background()
	_, _, err := m.SearchWithBestProvider(ctx, SearchRequest{Query: "test"})
	require.Error(t, err)
	used, err := m.GetUsage(ctx, ProviderTypeDeepSeek)
	require.NoError(t, err)
	require.Zero(t, used)
	status = 200
	_, name, err := m.SearchWithBestProvider(ctx, SearchRequest{Query: "test"})
	require.NoError(t, err)
	require.Equal(t, ProviderTypeDeepSeek, name)
	used, err = m.GetUsage(ctx, ProviderTypeDeepSeek)
	require.NoError(t, err)
	require.Equal(t, int64(1), used)
	_, _, err = m.SearchWithBestProvider(ctx, SearchRequest{Query: "test"})
	require.Error(t, err)
	require.Equal(t, 2, calls)
	// Admin tests deliberately bypass quota, including an exhausted quota.
	_, _, err = m.TestSearch(ctx, SearchRequest{Query: "test"})
	require.NoError(t, err)
	used, err = m.GetUsage(ctx, ProviderTypeDeepSeek)
	require.NoError(t, err)
	require.Equal(t, int64(1), used)
}

type deepSeekUnreadBody struct{ read, closed bool }

func (b *deepSeekUnreadBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("fictional-key must not be read")
}
func (b *deepSeekUnreadBody) Close() error { b.closed = true; return nil }

func TestDeepSeekProvider_Search_ClosesHTTPErrorWithoutReadingBody(t *testing.T) {
	body := &deepSeekUnreadBody{}
	client := &http.Client{Transport: deepSeekRoundTripFunc(func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: 401, Body: body}, nil })}
	_, err := NewDeepSeekProvider("fictional-key", client).Search(context.Background(), SearchRequest{Query: "test"})
	require.EqualError(t, err, "deepseek: status 401")
	require.False(t, body.read)
	require.True(t, body.closed)
}

func TestDeepSeekProvider_Search_EnforcesResultCount(t *testing.T) {
	client := &http.Client{Transport: deepSeekRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return deepSeekTestResponse(200, `{"results":[{"title":"one"},{"title":"two"}]}`), nil
	})}
	resp, err := NewDeepSeekProvider("fictional-key", client).Search(context.Background(), SearchRequest{Query: "test", MaxResults: 1})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	require.Equal(t, "one", resp.Results[0].Title)
}
