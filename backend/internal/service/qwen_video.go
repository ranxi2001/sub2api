package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const wanVideoModel = "wan3.0-video"

// WanVideoUnitPrice snapshots the configured tariff before an upstream task
// can incur a charge. The effective group multiplier is applied at settlement.
func (s *OpenAIGatewayService) WanVideoUnitPrice(ctx context.Context, key *APIKey, model, resolution string) *float64 {
	price := s.resolveOpenAIChannelPricing(ctx, model, key)
	if price == nil || price.Mode != BillingModeVideo {
		return nil
	}
	unit := s.resolver.GetRequestTierPrice(price, resolution)
	if unit <= 0 {
		unit = price.DefaultPerRequestPrice
	}
	if unit <= 0 {
		return nil
	}
	return &unit
}

type wanMedia struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}
type wanCreate struct {
	Model string `json:"model"`
	Input struct {
		Prompt string     `json:"prompt"`
		Media  []wanMedia `json:"media,omitempty"`
	} `json:"input"`
	Parameters struct {
		Resolution string `json:"resolution"`
		Duration   int    `json:"duration"`
		Ratio      string `json:"ratio,omitempty"`
		Audio      *bool  `json:"audio,omitempty"`
	} `json:"parameters"`
}

// The fixed-duration supplier rejects null media and an empty aspect ratio.
func (input wanCreate) fixedDurationBody() ([]byte, error) {
	payload := map[string]any{"model": input.Model, "prompt": input.Input.Prompt, "duration": input.Parameters.Duration, "resolution": input.Parameters.Resolution}
	if len(input.Input.Media) > 0 {
		payload["media"] = input.Input.Media
	}
	if input.Parameters.Ratio != "" {
		payload["aspect_ratio"] = input.Parameters.Ratio
	}
	return json.Marshal(payload)
}

// ParseWanVideoRequest accepts the gateway video JSON contract and normalizes the
// exact prompt and reference images forwarded to the moderation service.
func ParseWanVideoRequest(contentType string, body []byte) (GrokMediaRequestInfo, error) {
	var info GrokMediaRequestInfo
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil || mt != "application/json" {
		return info, errors.New("wan video requires application/json")
	}
	_, info, err = parseWanCreate(body)
	return info, err
}
func parseWanCreate(body []byte) (wanCreate, GrokMediaRequestInfo, error) {
	var out wanCreate
	var info GrokMediaRequestInfo
	var in struct {
		Model      string      `json:"model"`
		Prompt     string      `json:"prompt"`
		Seconds    json.Number `json:"seconds"`
		Duration   json.Number `json:"duration"`
		Resolution string      `json:"resolution"`
		Size       string      `json:"size"`
		Ratio      string      `json:"aspect_ratio"`
		Audio      *bool       `json:"audio"`
		Media      []wanMedia  `json:"media"`
	}
	object := gjson.ParseBytes(body)
	if err := validateOpenAIVideoJSONFields(object, "model", "prompt", "seconds", "duration", "resolution", "size", "aspect_ratio", "audio", "media"); err != nil {
		return out, info, err
	}
	for _, ref := range object.Get("media").Array() {
		if err := validateOpenAIVideoJSONFields(ref, "type", "url"); err != nil {
			return out, info, err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if !json.Valid(body) || decoder.Decode(&in) != nil || in.Model != wanVideoModel || strings.TrimSpace(in.Prompt) == "" {
		return out, info, errors.New("invalid Wan video request: supply model and prompt")
	}
	seconds := in.Seconds
	if seconds == "" {
		seconds = in.Duration
	}
	if seconds == "" {
		seconds = "5"
	}
	sec, err := seconds.Int64()
	if err != nil || sec < 2 || sec > 30 || (in.Duration != "" && in.Duration != seconds) {
		return out, info, errors.New("wan duration must be 2 to 30 seconds")
	}
	resolution := strings.ToLower(in.Resolution)
	if in.Size != "" {
		dims := strings.Split(in.Size, "x")
		if len(dims) != 2 {
			return out, info, errors.New("invalid video size")
		}
		w, e1 := strconv.Atoi(dims[0])
		h, e2 := strconv.Atoi(dims[1])
		if e1 != nil || e2 != nil || min(w, h) <= 0 {
			return out, info, errors.New("invalid video size")
		}
		tier, ok := LookupVideoBillingResolution(strconv.Itoa(min(w, h)))
		if !ok || resolution != "" && tier != resolution {
			return out, info, errors.New("size and resolution must agree")
		}
		resolution = tier
		if in.Ratio == "" {
			if w == h {
				in.Ratio = "1:1"
			} else if w > h {
				in.Ratio = "16:9"
			} else {
				in.Ratio = "9:16"
			}
		}
	}
	if resolution == "" {
		resolution = "720p"
	}
	if resolution != "480p" && resolution != "720p" && resolution != "1080p" {
		return out, info, errors.New("unsupported Wan resolution")
	}
	for _, m := range in.Media {
		parsed, err := url.Parse(m.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
			return out, info, errors.New("invalid Wan reference URL")
		}
		switch m.Type {
		case "first_frame", "last_frame", "reference_image":
			info.InputImageURLs = append(info.InputImageURLs, m.URL)
		case "reference_video", "reference_audio":
		default:
			return out, info, errors.New("unsupported Wan reference type")
		}
	}
	switch in.Ratio {
	case "", "adaptive", "16:9", "9:16", "4:3", "3:4", "1:1", "21:9":
	default:
		return out, info, errors.New("unsupported Wan aspect ratio")
	}
	out.Model = in.Model
	out.Input.Prompt = in.Prompt
	out.Input.Media = in.Media
	out.Parameters.Duration = int(sec)
	out.Parameters.Resolution = strings.ToUpper(resolution)
	out.Parameters.Ratio = in.Ratio
	out.Parameters.Audio = in.Audio
	info.Model, info.Prompt, info.Resolution, info.DurationSeconds, info.N = in.Model, in.Prompt, resolution, int(sec), 1
	return out, info, nil
}

func (s *OpenAIGatewayService) forwardWanVideo(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, taskID, upstreamID string, body []byte, info GrokMediaRequestInfo) (*OpenAIForwardResult, *OpenAIVideoCreation, error) {
	protocol := account.GetCredential("qwen_video_protocol")
	native := protocol == "dashscope"
	base, err := s.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return nil, nil, err
	}
	target, err := url.Parse(buildOpenAIEndpointURL(base, "/v1/videos"))
	if err != nil {
		return nil, nil, err
	}
	result := &OpenAIForwardResult{ResponseID: taskID}
	method := http.MethodGet
	if native {
		root := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(base, "/"), "/compatible-mode/v1"), "/api/v1"), "/v1")
		target, err = url.Parse(root + "/api/v1/services/aigc/video-generation/video-synthesis")
		if err != nil {
			return nil, nil, err
		}
	}
	if endpoint == OpenAIVideoEndpointCreate {
		method = http.MethodPost
		input, _, err := parseWanCreate(body)
		if err != nil {
			return nil, nil, err
		}
		if protocol == "fixed_duration" {
			if input.Parameters.Audio != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "This supplier does not support the audio switch"}})
				return nil, nil, errors.New("unsupported audio switch")
			}
			for _, media := range input.Input.Media {
				if media.Type == "reference_video" {
					c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "This video supplier does not report reference video usage"}})
					return nil, nil, errors.New("unmetered reference video")
				}
			}
			// The supplier omits metering on completion. Send the exact duration and tier from the admission quote.
			body, err = input.fixedDurationBody()
			if err != nil {
				return nil, nil, err
			}
		}
		result.Model, result.BillingModel, result.UpstreamModel = info.Model, info.Model, account.GetMappedModel(info.Model)
		if result.UpstreamModel != wanVideoModel {
			return nil, nil, errors.New("wan video requires the wan3.0-video model mapping")
		}
		result.VideoResolution, result.VideoDurationSeconds = info.Resolution, info.DurationSeconds
		if native {
			body, err = json.Marshal(input)
			if err != nil {
				return nil, nil, err
			}
		}
	} else {
		if err := validateUpstreamPathSegment("Wan task ID", upstreamID); err != nil {
			return nil, nil, err
		}
		if native {
			target.Path = strings.TrimSuffix(target.Path, "/services/aigc/video-generation/video-synthesis") + "/tasks/" + upstreamID
		} else {
			target.Path += "/" + upstreamID
		}
	}
	token := strings.TrimSpace(account.GetCredential("api_key"))
	if token == "" {
		return nil, nil, errors.New("missing Wan API key")
	}
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	account.ApplyHeaderOverridesWithContext(req.Context(), req.Header)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		if native {
			req.Header.Set("X-DashScope-Async", "enable")
		}
	}
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	start := time.Now()
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		return nil, nil, errors.New("wan upstream transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	result.Duration = time.Since(start)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, result.Duration.Milliseconds())
	raw, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		c.JSON(resp.StatusCode, gin.H{"error": gin.H{"type": "upstream_error", "message": "Wan upstream rejected the request"}})
		return nil, nil, fmt.Errorf("wan upstream HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode >= 300 || !gjson.ValidBytes(raw) {
		return nil, nil, errors.New("invalid Wan response")
	}
	if protocol == "fixed_duration" && endpoint != OpenAIVideoEndpointCreate {
		raw, err = wanFixedDurationReceipt(raw, info)
		if err != nil {
			return nil, nil, err
		}
	}
	view, err := parseWanTask(raw, native)
	if err != nil {
		return nil, nil, err
	}
	c.Header("Cache-Control", "private, no-store")
	if endpoint == OpenAIVideoEndpointCreate {
		result.ResponseID = openAIVideoTaskPrefix + uuid.NewString()
		view.ID = result.ResponseID
		// Status and content are released only after a bound lookup settles the job.
		view.Status = "queued"
		view.URL = ""
		view.BilledSeconds = 0
		return result, &OpenAIVideoCreation{UpstreamTaskID: view.upstreamID, WriteResponse: func() { c.JSON(http.StatusOK, view) }}, nil
	}
	if view.upstreamID != upstreamID {
		return nil, nil, errors.New("wan returned a different task")
	}
	view.ID = taskID
	if view.Status == "completed" {
		result.VideoCount = 1
		result.VideoDurationSeconds = int(math.Ceil(view.BilledSeconds))
		if info.Resolution != "" && info.Resolution != view.Resolution {
			return nil, nil, errors.New("completed Wan resolution differs from accepted quote")
		}
		result.WanBilledSeconds = view.BilledSeconds
		result.VideoResolution = view.Resolution
	}
	if endpoint == OpenAIVideoEndpointContent {
		if view.Status != "completed" {
			c.JSON(http.StatusConflict, gin.H{"error": gin.H{"message": "Video is not ready"}})
			return result, nil, nil
		}
		c.Header("Location", view.URL)
		c.Status(http.StatusTemporaryRedirect)
	} else {
		c.JSON(http.StatusOK, view)
	}
	return result, nil, nil
}

// A fixed-duration supplier does not return unit counts. Only jobs created
// without input video can use their server-owned submission duration as the charge.
func wanFixedDurationReceipt(raw []byte, info GrokMediaRequestInfo) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if gjson.GetBytes(raw, "status").String() != "completed" {
		return raw, nil
	}
	if info.DurationSeconds < 2 || info.DurationSeconds > 30 {
		return nil, errors.New("missing fixed-duration task snapshot")
	}
	if _, ok := LookupVideoBillingResolution(info.Resolution); !ok {
		return nil, errors.New("missing fixed-duration task resolution")
	}
	if _, ok := obj["billed_seconds"]; !ok {
		obj["billed_seconds"] = json.RawMessage(strconv.Itoa(info.DurationSeconds))
	}
	if _, ok := obj["resolution"]; !ok {
		b, err := json.Marshal(info.Resolution)
		if err != nil {
			return nil, err
		}
		obj["resolution"] = b
	}
	if _, ok := obj["url"]; !ok {
		for _, name := range []string{"video_url", "result_url"} {
			if v, ok := obj[name]; ok {
				obj["url"] = v
				break
			}
		}
	}
	return json.Marshal(obj)
}

type wanTask struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	URL           string  `json:"url,omitempty"`
	BilledSeconds float64 `json:"billed_seconds,omitempty"`
	Resolution    string  `json:"resolution,omitempty"`
	upstreamID    string
}

func parseWanTask(raw []byte, native bool) (wanTask, error) {
	var t wanTask
	if native {
		d := gjson.ParseBytes(raw)
		t.upstreamID = d.Get("output.task_id").String()
		switch d.Get("output.task_status").String() {
		case "PENDING":
			t.Status = "queued"
		case "RUNNING":
			t.Status = "in_progress"
		case "SUCCEEDED":
			t.Status = "completed"
		case "FAILED", "CANCELED", "UNKNOWN":
			t.Status = "failed"
		}
		if t.Status == "completed" {
			t.URL = d.Get("output.video_url").String()
			t.BilledSeconds = d.Get("usage.duration").Float()
			t.Resolution, _ = LookupVideoBillingResolution(d.Get("usage.SR").String())
		}
	} else {
		if err := json.Unmarshal(raw, &t); err != nil {
			return t, errors.New("invalid video relay response")
		}
		t.upstreamID = t.ID
	}
	if strings.TrimSpace(t.upstreamID) != t.upstreamID || validateUpstreamPathSegment("video task ID", t.upstreamID) != nil {
		return t, errors.New("invalid Wan task ID")
	}
	switch t.Status {
	case "queued", "in_progress", "failed":
		t.URL = ""
		t.BilledSeconds = 0
	case "completed":
		u, err := url.Parse(t.URL)
		_, ok := LookupVideoBillingResolution(t.Resolution)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || !ok || !(t.BilledSeconds > 0 && t.BilledSeconds <= 45) {
			return t, errors.New("invalid completed Wan task or missing usage")
		}
	default:
		return t, errors.New("unknown Wan task state")
	}
	return t, nil
}
