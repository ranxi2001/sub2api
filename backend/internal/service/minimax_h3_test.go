//go:build unit

package service

import (
	"context"
	"io"
	"math"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func miniMaxH3TestAccount() *Account {
	return &Account{ID: 9, Platform: PlatformMiniMax, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "test-upstream-key", "base_url": "https://relay.example/v1", "account_mode": AccountModePayG,
		"model_mapping": map[string]any{"video-alias": "MiniMax-H3"},
	}}
}

func TestMiniMaxH3ForwardPreservesProtocol(t *testing.T) {
	body := []byte(`{"model":"video-alias","prompt":"waves","seconds":"1","size":"1344x768","mode":"frames","metadata":{"ratio":"16:9","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="},"role":"first_frame"},{"type":"video_url","video_url":{"url":"https://example.com/reference.mp4"},"role":"reference_video"}]},"future":{"keep":true}}`)
	upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"id":"task-1","status":"queued"}`)}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	c, w := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
	result, err := svc.ForwardMiniMaxH3(context.Background(), c, miniMaxH3TestAccount(), MiniMaxH3EndpointCreate, "", body)
	require.NoError(t, err)
	require.Equal(t, "https://relay.example/v1/videos", upstream.request.URL.String())
	require.Equal(t, "Bearer test-upstream-key", upstream.request.Header.Get("Authorization"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.request.Context()))
	forwarded, err := io.ReadAll(upstream.request.Body)
	require.NoError(t, err)
	require.Equal(t, "MiniMax-H3", gjson.GetBytes(forwarded, "model").String())
	for _, field := range []string{"prompt", "seconds", "size", "mode", "metadata", "future"} {
		require.Equal(t, gjson.GetBytes(body, field).Raw, gjson.GetBytes(forwarded, field).Raw)
	}
	require.Equal(t, "minimax-h3:task-1", result.ResponseID)
	require.Equal(t, "video-alias", result.BillingModel)
	require.Equal(t, "768p", result.VideoResolution)
	require.Equal(t, 1, result.VideoDurationSeconds)
	require.Zero(t, result.VideoCount, "async acceptance must not charge")
	require.JSONEq(t, `{"id":"task-1","status":"queued"}`, w.Body.String())
}

func TestMiniMaxH3ExplicitVideoModelMapping(t *testing.T) {
	for _, tc := range []struct {
		name, model, source, target, want string
	}{
		{"standard model", "MiniMax-H3", "", "", "MiniMax-H3"},
		{"custom alias", "h3-video", "h3-video", "relay-video-v2", "relay-video-v2"},
		{"identity mapping", "relay-video-v2", "relay-video-v2", "relay-video-v2", "relay-video-v2"},
		{"unmapped model", "relay-video-v2", "", "", ""},
		{"wildcard only", "h3-video", "h3-*", "relay-video-v2", ""},
		{"blank target", "h3-video", "h3-video", " ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := miniMaxH3TestAccount()
			delete(account.Credentials, "model_mapping")
			if tc.source != "" {
				account.Credentials["model_mapping"] = map[string]any{tc.source: tc.target}
			}
			upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"id":"task-1","status":"queued"}`)}
			svc := &OpenAIGatewayService{httpUpstream: upstream}
			c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
			body := []byte(`{"model":"` + tc.model + `","prompt":"waves","seconds":"1"}`)
			result, err := svc.ForwardMiniMaxH3(context.Background(), c, account, MiniMaxH3EndpointCreate, "", body)
			if tc.want == "" {
				require.Error(t, err)
				require.Empty(t, upstream.requests)
				return
			}
			require.NoError(t, err)
			forwarded, err := io.ReadAll(upstream.request.Body)
			require.NoError(t, err)
			require.Equal(t, tc.want, gjson.GetBytes(forwarded, "model").String())
			require.Equal(t, tc.model, result.BillingModel)
		})
	}
}

func TestMiniMaxH3ValidationAndURLs(t *testing.T) {
	for _, body := range []string{`{`, `[]`, `{}`, `{"model":"MiniMax-H3","prompt":"x","seconds":1}`, `{"model":"MiniMax-H3","prompt":"x","seconds":"0"}`, `{"model":"MiniMax-H3","prompt":"x","seconds":"16"}`, `{"model":"MiniMax-H3","prompt":"x","seconds":"01"}`, `{"model":"MiniMax-H3","prompt":" ","seconds":"1"}`, `{"model":"MiniMax-H3","prompt":"x","seconds":"1","n":2}`} {
		_, err := ParseMiniMaxH3Request([]byte(body))
		require.Error(t, err, body)
	}
	for _, base := range []string{"https://relay.example", "https://relay.example/v1/"} {
		target, err := buildMiniMaxH3URL(base, MiniMaxH3EndpointCreate, "")
		require.NoError(t, err)
		require.Equal(t, "https://relay.example/v1/videos", target)
	}
	for _, id := range []string{"", "..", "a/b", "a?b", "a#b", "%2e%2e"} {
		_, err := buildMiniMaxH3URL("https://relay.example", MiniMaxH3EndpointStatus, id)
		require.Error(t, err, id)
	}
	a := miniMaxH3TestAccount()
	require.True(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityMiniMaxH3))
	a.Credentials["account_mode"] = AccountModeCoding
	require.False(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityMiniMaxH3))
	a.Credentials["account_mode"] = AccountModePayG
	a.Platform = PlatformGrok
	require.False(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityMiniMaxH3))
}

func TestMiniMaxH3StatusStates(t *testing.T) {
	const outputURL = `"url":"https://cdn.example/output.mp4"`
	const metadataURL = `"metadata":{` + outputURL + `}`
	for _, tc := range []struct {
		name, status, output string
		videos               int
	}{
		{"queued", "queued", metadataURL, 0},
		{"in progress", "in_progress", metadataURL, 0},
		{"unknown", "unknown", metadataURL, 0},
		{"failed", "failed", outputURL, 0},
		{"completed metadata URL", "completed", metadataURL, 1},
		{"completed top-level URL", "completed", outputURL, 1},
		{"completed insecure URL", "completed", `"url":"http://cdn.example/output.mp4"`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"id":"task-1","status":"` + tc.status + `","seconds":"15",` + tc.output + `}`
			upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(body)}
			svc := &OpenAIGatewayService{httpUpstream: upstream}
			c, w := grokMediaContentTestContext(http.MethodGet, "/v1/videos/task-1", nil)
			result, err := svc.ForwardMiniMaxH3(context.Background(), c, miniMaxH3TestAccount(), MiniMaxH3EndpointStatus, MiniMaxH3TaskKey("task-1"), nil)
			require.NoError(t, err)
			require.JSONEq(t, body, w.Body.String())
			require.Equal(t, "/v1/videos/task-1", upstream.request.URL.Path)
			require.Equal(t, tc.videos, result.VideoCount)
			require.Zero(t, result.VideoDurationSeconds, "status must not override requested seconds")
		})
	}
}

func TestMiniMaxH3ContentDoesNotFollowRedirect(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse("")}
	upstream.response.StatusCode = http.StatusFound
	upstream.response.Header.Set("Location", "https://cdn.example/output.mp4?signature=fake")
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	c, w := grokMediaContentTestContext(http.MethodGet, "/v1/videos/task-1/content", nil)
	result, err := svc.ForwardMiniMaxH3(context.Background(), c, miniMaxH3TestAccount(), MiniMaxH3EndpointContent, MiniMaxH3TaskKey("task-1"), nil)
	require.NoError(t, err)
	c.Writer.WriteHeaderNow()
	require.Equal(t, http.StatusFound, w.Code)
	require.Equal(t, upstream.response.Header.Get("Location"), w.Header().Get("Location"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.request.Context()))
	require.Len(t, upstream.requests, 1, "never send the account key to the output host")
	require.Equal(t, 1, result.VideoCount)
}

func TestMiniMaxH3UpstreamErrorIsNotRetried(t *testing.T) {
	for _, status := range []int{400, 429, 503} {
		upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"error":{"message":"upstream failure"}}`)}
		upstream.response.StatusCode = status
		svc := &OpenAIGatewayService{httpUpstream: upstream}
		c, w := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
		_, err := svc.ForwardMiniMaxH3(context.Background(), c, miniMaxH3TestAccount(), MiniMaxH3EndpointCreate, "", []byte(`{"model":"MiniMax-H3","prompt":"waves","seconds":"1"}`))
		require.Error(t, err)
		require.Equal(t, status, w.Code)
		require.Len(t, upstream.requests, 1)
	}
}

func TestMiniMaxH3TariffsAndBillingSnapshot(t *testing.T) {
	perSecond := map[string]map[string]float64{MiniMaxH3PriceFamily: {MiniMaxH3PerSecond: 0.02}}
	perRequest := map[string]map[string]float64{MiniMaxH3PriceFamily: {"request_5s": 0.08}}
	base, err := QuoteMiniMaxH3Video(perSecond, 5)
	require.NoError(t, err)
	require.InDelta(t, 0.1, base, 1e-9)
	base, err = QuoteMiniMaxH3Video(perRequest, 5)
	require.NoError(t, err)
	require.InDelta(t, 0.08, base, 1e-9)
	_, err = QuoteMiniMaxH3Video(perRequest, 15)
	require.ErrorContains(t, err, `seconds="5"`)
	for _, tariff := range []map[string]map[string]float64{nil, {MiniMaxH3PriceFamily: {"480p": 0.1}}, {MiniMaxH3PriceFamily: {MiniMaxH3PerSecond: -1}}, {MiniMaxH3PriceFamily: {MiniMaxH3PerSecond: math.Inf(1)}}, {MiniMaxH3PriceFamily: {MiniMaxH3PerSecond: 0.1, "request_5s": 0.3}}} {
		_, err := QuoteMiniMaxH3Video(tariff, 5)
		require.Error(t, err)
	}
	require.Equal(t, perRequest, NormalizeVideoModelPrices(perRequest))
	svc := &OpenAIGatewayService{}
	result := &OpenAIForwardResult{VideoCount: 1, MiniMaxH3BaseCost: &base, VideoDurationSeconds: 5}
	cost := svc.calculateOpenAIVideoCost(context.Background(), "mapped-name", &APIKey{}, result, 0.1)
	require.InDelta(t, 0.008, cost.ActualCost, 1e-9)
	key := &APIKey{Group: &Group{RateMultiplier: 0.1, VideoRateIndependent: true, VideoRateMultiplier: 100}}
	estimate, ok := (inflightEstimateDeps{}).estimate(context.Background(), key, InflightEstimateRequest{MiniMaxH3BaseCost: &base})
	require.True(t, ok)
	require.InDelta(t, cost.ActualCost, estimate, 1e-9)
}
