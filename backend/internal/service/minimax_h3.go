package service

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	MiniMaxH3EndpointCreate           GrokMediaEndpoint        = "minimax_h3_create"
	MiniMaxH3EndpointStatus           GrokMediaEndpoint        = "minimax_h3_status"
	MiniMaxH3EndpointContent          GrokMediaEndpoint        = "minimax_h3_content"
	OpenAIEndpointCapabilityMiniMaxH3 OpenAIEndpointCapability = "minimax_h3"
	MiniMaxH3PriceFamily                                       = "minimax-h3"
	MiniMaxH3PerSecond                                         = "per_second"
)

func (e GrokMediaEndpoint) IsMiniMaxH3() bool {
	return e == MiniMaxH3EndpointCreate || e == MiniMaxH3EndpointStatus || e == MiniMaxH3EndpointContent
}

func MiniMaxH3TaskKey(id string) string { return "minimax-h3:" + strings.TrimSpace(id) }

// H3 uses this relay's OpenAI Videos protocol, not MiniMax's native video API.
// Keep the original JSON so frame/reference media and future fields survive.
func ParseMiniMaxH3Request(body []byte) (GrokMediaRequestInfo, error) {
	info := GrokMediaRequestInfo{N: 1, Resolution: "768p"}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return info, fmt.Errorf("request body must be a JSON object")
	}
	model, prompt, seconds := gjson.GetBytes(body, "model"), gjson.GetBytes(body, "prompt"), gjson.GetBytes(body, "seconds")
	if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		return info, fmt.Errorf("model is required")
	}
	if prompt.Type != gjson.String || strings.TrimSpace(prompt.String()) == "" || utf8.RuneCountInString(prompt.String()) > 7000 {
		return info, fmt.Errorf("prompt must contain 1 to 7000 characters")
	}
	duration, err := strconv.Atoi(seconds.String())
	if seconds.Type != gjson.String || err != nil || duration < 1 || duration > 15 || seconds.String() != strconv.Itoa(duration) {
		return info, fmt.Errorf("seconds must be an integer string from \"1\" to \"15\"")
	}
	// H3 creates one task/video. Reject batching fields rather than undercharge.
	if n := gjson.GetBytes(body, "n"); n.Exists() && (n.Type != gjson.Number || n.Float() != 1) {
		return info, fmt.Errorf("MiniMax-H3 supports one video per request")
	}
	info.Model, info.Prompt, info.DurationSeconds = strings.TrimSpace(model.String()), prompt.String(), duration
	info.Size = gjson.GetBytes(body, "size").String()
	for _, item := range gjson.GetBytes(body, "metadata.content").Array() {
		if item.Get("type").String() == "image_url" {
			info.InputImageURLs = append(info.InputImageURLs, item.Get("image_url.url").String())
		}
	}
	return info, nil
}

// Reuse the group's versioned media JSONB: one per_second tariff, or a fixed
// request_<seconds>s tariff. No currency conversion or resolution fallback.
func miniMaxH3RequestSeconds(key string) (int, bool) {
	if !strings.HasPrefix(key, "request_") || !strings.HasSuffix(key, "s") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(key, "request_"), "s"))
	return n, err == nil && n >= 1 && n <= 15 && key == fmt.Sprintf("request_%ds", n)
}

func QuoteMiniMaxH3Video(prices map[string]map[string]float64, seconds int) (float64, error) {
	if seconds < 1 || seconds > 15 {
		return 0, fmt.Errorf("seconds must be between 1 and 15")
	}
	tariff := prices[MiniMaxH3PriceFamily]
	if len(tariff) != 1 {
		return 0, fmt.Errorf("MiniMax-H3 video is disabled or its group price is not configured")
	}
	for key, price := range tariff {
		if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			break
		}
		if key == MiniMaxH3PerSecond {
			total := price * float64(seconds)
			if !math.IsInf(total, 0) {
				return total, nil
			}
		}
		if duration, ok := miniMaxH3RequestSeconds(key); ok {
			if seconds != duration {
				return 0, fmt.Errorf("this group's per-request video price requires seconds=\"%d\"", duration)
			}
			return price, nil
		}
	}
	return 0, fmt.Errorf("invalid MiniMax-H3 group video price")
}

func buildMiniMaxH3URL(base string, endpoint GrokMediaEndpoint, taskID string) (string, error) {
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	base += "/videos"
	if endpoint != MiniMaxH3EndpointCreate {
		if err := validateUpstreamPathSegment("MiniMax-H3 task ID", taskID); err != nil || strings.TrimSpace(taskID) == "" {
			return "", fmt.Errorf("invalid MiniMax-H3 task ID")
		}
		base += "/" + taskID
		if endpoint == MiniMaxH3EndpointContent {
			base += "/content"
		}
	}
	return base, nil
}

func validMiniMaxH3OutputURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func (s *OpenAIGatewayService) ForwardMiniMaxH3(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, taskID string, body []byte) (*OpenAIForwardResult, error) {
	if err := CheckUpstreamModeration(ctx, c, account); err != nil {
		return nil, err
	}

	if !endpoint.IsMiniMaxH3() || !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityMiniMaxH3) {
		return nil, fmt.Errorf("MiniMax-H3 requires a MiniMax pay-as-you-go API key account with a custom base URL")
	}
	base, err := s.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return nil, err
	}
	target, err := buildMiniMaxH3URL(base, endpoint, strings.TrimPrefix(taskID, "minimax-h3:"))
	if err != nil {
		return nil, err
	}
	method := http.MethodGet
	result := &OpenAIForwardResult{ResponseID: taskID}
	if endpoint == MiniMaxH3EndpointCreate {
		info, err := ParseMiniMaxH3Request(body)
		if err != nil {
			return nil, err
		}
		result.Model, result.BillingModel, result.UpstreamModel = info.Model, info.Model, account.GetMappedModel(info.Model)
		if !strings.EqualFold(result.UpstreamModel, "MiniMax-H3") {
			return nil, fmt.Errorf("this video relay only supports MiniMax-H3")
		}
		result.VideoResolution, result.VideoDurationSeconds = info.Resolution, info.DurationSeconds
		body, err = sjson.SetBytes(body, "model", result.UpstreamModel)
		if err != nil {
			return nil, err
		}
		method = http.MethodPost
	}
	token := strings.TrimSpace(account.GetCredential("api_key"))
	if token == "" {
		return nil, fmt.Errorf("MiniMax-H3 account missing api_key")
	}
	started := time.Now()
	// Never follow credential-bearing redirects, including same-host redirects.
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	account.ApplyHeaderOverrides(req.Header)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("MiniMax-H3 upstream transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	result.Duration, result.ResponseHeaders = time.Since(started), resp.Header.Clone()
	if endpoint == MiniMaxH3EndpointContent && resp.StatusCode == http.StatusFound {
		location := resp.Header.Get("Location")
		if !validMiniMaxH3OutputURL(location) {
			return nil, fmt.Errorf("MiniMax-H3 content response has an invalid output URL")
		}
		result.VideoCount = 1
		c.Header("Location", location)
		c.Header("Cache-Control", "private, no-store")
		c.Status(http.StatusFound)
		return result, nil
	}
	responseBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	// An ambiguous POST must never become an automatic account-switch retry.
	if resp.StatusCode >= 300 {
		if resp.StatusCode < 400 {
			return nil, fmt.Errorf("unexpected MiniMax-H3 redirect")
		}
		writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
		return nil, fmt.Errorf("MiniMax-H3 upstream status %d", resp.StatusCode)
	}
	if !gjson.ValidBytes(responseBody) || !gjson.ParseBytes(responseBody).IsObject() {
		return nil, fmt.Errorf("invalid MiniMax-H3 response")
	}
	switch endpoint {
	case MiniMaxH3EndpointCreate:
		id := gjson.GetBytes(responseBody, "id").String()
		if id == "" {
			id = gjson.GetBytes(responseBody, "task_id").String()
		}
		if _, err := buildMiniMaxH3URL(base, MiniMaxH3EndpointStatus, id); err != nil {
			return nil, err
		}
		result.ResponseID = MiniMaxH3TaskKey(id)
	case MiniMaxH3EndpointStatus:
		if gjson.GetBytes(responseBody, "status").String() == "completed" && validMiniMaxH3OutputURL(gjson.GetBytes(responseBody, "metadata.url").String()) {
			result.VideoCount = 1
		}
	}
	c.Header("Cache-Control", "private, no-store")
	writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
	return result, nil
}
