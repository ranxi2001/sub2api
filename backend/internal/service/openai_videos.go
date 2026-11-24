package service

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	OpenAIVideoEndpointCreate      GrokMediaEndpoint        = "openai_video_create"
	OpenAIVideoEndpointStatus      GrokMediaEndpoint        = "openai_video_status"
	OpenAIVideoEndpointContent     GrokMediaEndpoint        = "openai_video_content"
	OpenAIEndpointCapabilityVideos OpenAIEndpointCapability = "videos"
	openAIVideoTaskPrefix                                   = "video_proxy_"
)

func (e GrokMediaEndpoint) IsOpenAIVideo() bool {
	return e == OpenAIVideoEndpointCreate || e == OpenAIVideoEndpointStatus || e == OpenAIVideoEndpointContent
}

func IsOpenAIVideoTaskID(id string) bool {
	raw, ok := strings.CutPrefix(id, openAIVideoTaskPrefix)
	if !ok || len(raw) != 36 {
		return false
	}
	_, err := uuid.Parse(raw)
	return err == nil
}

func (s *OpenAIGatewayService) HasOpenAIVideoAccount(ctx context.Context, groupID *int64, platform string) (bool, error) {
	accounts, err := s.listSchedulableAccounts(ctx, groupID, platform)
	if err != nil {
		return false, err
	}
	for i := range accounts {
		if accounts[i].SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideos) {
			return true, nil
		}
	}
	return false, nil
}

func ParseOpenAIVideoRequest(contentType string, body []byte) (GrokMediaRequestInfo, error) {
	info := GrokMediaRequestInfo{N: 1}
	mediaType, _, err := mime.ParseMediaType(contentType)
	isMultipart := err == nil && mediaType == "multipart/form-data"
	fields := make(map[string]string)
	capture := func(name, value string, isFile bool) error {
		canonical := openAIVideoFieldName(name, "model", "seconds", "duration", "n", "size", "resolution", "resolution_name",
			"prompt", "image", "images", "reference_images", "image_url", "mask", "mask_image_url", "input_reference", "first_frame", "last_frame", "metadata")
		if prefix, _, indexed := strings.Cut(name, "["); isMultipart && indexed && strings.EqualFold(prefix, "image") {
			if prefix != "image" || !isFile {
				return fmt.Errorf("invalid video image field name")
			}
			return nil
		}
		if canonical == "" {
			return nil
		}
		// Multipart image parts accumulate in the moderation input. JSON keys and
		// scalar multipart fields must be unique: upstream decoders may pick a
		// different occurrence than the moderation parser.
		repeatable := isMultipart && (canonical == "image" || canonical == "image_url" ||
			(isFile && (canonical == "images" || canonical == "input_reference" || canonical == "first_frame" || canonical == "last_frame")))
		seenName := canonical
		if canonical == "mask_image_url" {
			seenName = "mask"
		}
		if _, duplicate := fields[seenName]; canonical != name || (duplicate && !repeatable) {
			return fmt.Errorf("invalid or repeated video field %q", canonical)
		}
		fields[seenName] = strings.TrimSpace(value)
		if isFile {
			switch canonical {
			case "image", "images", "mask", "input_reference", "first_frame", "last_frame":
				return nil
			default:
				return fmt.Errorf("video field %q must be a text part", canonical)
			}
		}
		if isMultipart {
			switch canonical {
			case "input_reference", "metadata":
				nested := gjson.Parse(value)
				if !gjson.Valid(value) || !nested.IsObject() {
					return fmt.Errorf("video field %q must be a JSON object", canonical)
				}
				return parseOpenAIVideoNestedImages(canonical, nested, &info)
			case "images", "reference_images", "first_frame", "last_frame":
				return fmt.Errorf("video field %q requires an image upload", canonical)
			}
		}
		return nil
	}

	switch {
	case contentType == "" || mediaType == "application/json":
		request := gjson.ParseBytes(body)
		if !gjson.ValidBytes(body) || !request.IsObject() || request.Get("model").Type != gjson.String {
			return info, fmt.Errorf("video request must be a JSON object with a string model")
		}
		request.ForEach(func(key, value gjson.Result) bool {
			err = capture(key.String(), value.String(), false)
			return err == nil
		})
		if err != nil {
			return info, err
		}
		parseGrokMediaJSONRequest(body, &info)
		for _, name := range []string{"image", "images", "reference_images", "mask"} {
			value := request.Get(name)
			images := []gjson.Result{value}
			if name != "mask" && value.IsArray() {
				images = value.Array()
			}
			for _, image := range images {
				if err := validateOpenAIVideoImageJSON(image); err != nil {
					return info, err
				}
			}
		}
		for _, name := range []string{"input_reference", "metadata"} {
			if err := parseOpenAIVideoNestedImages(name, request.Get(name), &info); err != nil {
				return info, err
			}
		}
	case err == nil && mediaType == "multipart/form-data":
		if err := parseGrokMediaMultipartRequest(contentType, body, &info, capture); err != nil {
			return info, err
		}
	default:
		return info, fmt.Errorf("video request requires JSON or multipart/form-data")
	}
	if info.Model == "" {
		return info, fmt.Errorf("model is required")
	}
	if n, present := fields["n"]; present && n != "1" {
		return info, fmt.Errorf("only one video per request is supported")
	}
	if seconds, duration := fields["seconds"], fields["duration"]; seconds != "" && duration != "" && seconds != duration {
		return info, fmt.Errorf("seconds and duration must agree")
	}
	// Video usage stores seconds as an integer. Keep the relay's duration intact
	// rather than applying the xAI-specific 15 second limit.
	duration := fields["seconds"]
	if duration == "" {
		duration = fields["duration"]
	}
	seconds, err := strconv.ParseInt(duration, 10, 32)
	if err != nil || seconds <= 0 {
		return info, fmt.Errorf("seconds must be a positive integer")
	}
	info.DurationSeconds = int(seconds)
	info.Resolution = openAIVideoResolution(fields["resolution"], fields["resolution_name"], info.Size)
	return info, nil
}

func openAIVideoFieldName(name string, fields ...string) string {
	for _, field := range fields {
		if strings.EqualFold(name, field) {
			return field
		}
	}
	return ""
}

// Check only keys used by moderation, leaving unrelated relay metadata intact.
func validateOpenAIVideoJSONFields(object gjson.Result, fields ...string) error {
	if !object.IsObject() {
		return nil
	}
	seen := make(map[string]bool)
	var err error
	object.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		if canonical := openAIVideoFieldName(name, fields...); canonical != "" {
			if canonical != name || seen[canonical] {
				err = fmt.Errorf("invalid or repeated video field %q", canonical)
				return false
			}
			seen[canonical] = true
		}
		return true
	})
	return err
}

func validateOpenAIVideoImageJSON(value gjson.Result) error {
	if err := validateOpenAIVideoJSONFields(value, "url", "image_url"); err != nil {
		return err
	}
	return validateOpenAIVideoJSONFields(value.Get("image_url"), "url")
}

func parseOpenAIVideoNestedImages(name string, value gjson.Result, info *GrokMediaRequestInfo) error {
	images := []gjson.Result{}
	switch name {
	case "input_reference":
		if err := validateOpenAIVideoJSONFields(value, "image_url"); err != nil {
			return err
		}
		images = append(images, value.Get("image_url"))
	case "metadata":
		if err := validateOpenAIVideoJSONFields(value, "content"); err != nil {
			return err
		}
		for _, item := range value.Get("content").Array() {
			if err := validateOpenAIVideoJSONFields(item, "image_url"); err != nil {
				return err
			}
			images = append(images, item.Get("image_url"))
		}
	}
	for _, image := range images {
		if err := validateOpenAIVideoImageJSON(image); err != nil {
			return err
		}
		if imageURL := extractGrokMediaImageURL(image); imageURL != "" {
			info.InputImageURLs = append(info.InputImageURLs, imageURL)
		}
	}
	return nil
}

func openAIVideoResolution(values ...string) string {
	tier := VideoBillingResolution480P
	for _, value := range values {
		if candidate, ok := LookupVideoBillingResolution(value); ok {
			switch candidate {
			case VideoBillingResolution1080P:
				return candidate
			case VideoBillingResolution720P:
				tier = candidate
			}
			continue
		}
		width, height, ok := strings.Cut(strings.ToLower(strings.TrimSpace(value)), "x")
		if !ok {
			continue
		}
		w, widthErr := strconv.Atoi(width)
		h, heightErr := strconv.Atoi(height)
		if widthErr != nil || heightErr != nil {
			continue
		}
		switch {
		case min(w, h) >= 1080:
			return VideoBillingResolution1080P
		case min(w, h) >= 720:
			tier = VideoBillingResolution720P
		}
	}
	return tier
}

func (r *OpenAIForwardResult) VideoBillingDuration() int {
	if IsOpenAIVideoTaskID(r.ResponseID) && r.VideoDurationSeconds > 0 {
		return r.VideoDurationSeconds
	}
	return NormalizeVideoBillingDurationSecondsOrDefault(r.VideoDurationSeconds)
}

// OpenAIVideoCreation defers the acknowledgement until the task binding and
// billing snapshot are saved. A client may start polling as soon as it has an ID.
type OpenAIVideoCreation struct {
	UpstreamTaskID string
	WriteResponse  func()
}

func (s *OpenAIGatewayService) ForwardOpenAIVideo(
	ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint,
	taskID, upstreamTaskID string, body []byte, contentType string, info GrokMediaRequestInfo,
) (*OpenAIForwardResult, *OpenAIVideoCreation, error) {
	if err := CheckUpstreamModeration(ctx, c, account); err != nil {
		return nil, nil, err
	}
	if !endpoint.IsOpenAIVideo() || !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideos) {
		return nil, nil, fmt.Errorf("OpenAI Videos requires an enabled API Key account")
	}
	if account.Platform == PlatformQwen {
		return s.forwardWanVideo(ctx, c, account, endpoint, taskID, upstreamTaskID, body, info)
	}
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
	if endpoint == OpenAIVideoEndpointCreate {
		method = http.MethodPost
		result.Model, result.BillingModel = info.Model, info.Model
		result.UpstreamModel = account.GetMappedModel(info.Model)
		result.VideoResolution, result.VideoDurationSeconds = info.Resolution, info.DurationSeconds
		if result.UpstreamModel != info.Model {
			body, contentType, err = rewriteOpenAIImagesModel(body, contentType, result.UpstreamModel)
			if err != nil {
				return nil, nil, err
			}
		}
	} else {
		if err := validateUpstreamPathSegment("video task ID", upstreamTaskID); err != nil {
			return nil, nil, err
		}
		target.Path += "/" + upstreamTaskID
		if endpoint == OpenAIVideoEndpointContent {
			target.Path += "/content"
		}
	}
	token := strings.TrimSpace(account.GetCredential("api_key"))
	if token == "" {
		return nil, nil, fmt.Errorf("OpenAI Videos account missing api_key")
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	account.ApplyHeaderOverridesWithContext(req.Context(), req.Header)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	if endpoint == OpenAIVideoEndpointContent {
		req.Header.Set("Accept", "video/*, application/octet-stream")
		if value := c.GetHeader("Range"); value != "" {
			req.Header.Set("Range", value)
		}
	}
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(start).Milliseconds())
	if err != nil {
		// The relay may already have accepted a paid job. Do not retry the POST.
		return nil, nil, fmt.Errorf("OpenAI Videos upstream transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	result.Duration, result.ResponseHeaders = time.Since(start), resp.Header.Clone()
	c.Header("Cache-Control", "private, no-store")
	if endpoint == OpenAIVideoEndpointContent {
		switch resp.StatusCode {
		case http.StatusOK, http.StatusPartialContent:
			mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			if !strings.HasPrefix(mediaType, "video/") && mediaType != "application/octet-stream" {
				return nil, nil, fmt.Errorf("invalid OpenAI Videos content type")
			}
			result.VideoCount = 1
			return result, nil, writeGrokMediaContentResponse(c, resp)
		case http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect:
			location, err := resp.Location()
			if err != nil || location.Scheme != "https" || location.Hostname() == "" || location.User != nil {
				return nil, nil, fmt.Errorf("invalid OpenAI Videos content redirect")
			}
			// Do not follow the redirect with the upstream API key.
			c.Header("Location", location.String())
			c.Status(resp.StatusCode)
			result.VideoCount = 1
			return result, nil, nil
		}
	}
	responseBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
		return nil, nil, fmt.Errorf("OpenAI Videos upstream status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 300 || endpoint == OpenAIVideoEndpointContent ||
		!gjson.ValidBytes(responseBody) || !gjson.ParseBytes(responseBody).IsObject() {
		return nil, nil, fmt.Errorf("invalid OpenAI Videos response")
	}

	id := gjson.GetBytes(responseBody, "id")
	if id.Type != gjson.String || id.String() != strings.TrimSpace(id.String()) || validateUpstreamPathSegment("video task ID", id.String()) != nil {
		return nil, nil, fmt.Errorf("invalid OpenAI Videos task ID")
	}
	if endpoint == OpenAIVideoEndpointCreate {
		result.ResponseID = openAIVideoTaskPrefix + uuid.NewString()
		upstreamTaskID = id.String()
	} else if id.String() != upstreamTaskID {
		return nil, nil, fmt.Errorf("OpenAI Videos returned a different task ID")
	}

	responseBody, err = sjson.SetBytes(responseBody, "id", result.ResponseID)
	if err != nil {
		return nil, nil, err
	}
	contentURL := grokMediaContentProxyURL(c, result.ResponseID)
	responseBody = rewriteGrokMediaVideoContentURLs(responseBody, upstreamTaskID, contentURL)
	resp.Header.Del("Content-Length")
	if resp.Header.Get("Location") != "" {
		resp.Header.Set("Location", strings.TrimSuffix(contentURL, "/content"))
	}
	if endpoint == OpenAIVideoEndpointCreate {
		return result, &OpenAIVideoCreation{
			UpstreamTaskID: upstreamTaskID,
			WriteResponse:  func() { writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter) },
		}, nil
	}
	if gjson.GetBytes(responseBody, "status").String() == "completed" {
		result.VideoCount = 1
	}
	writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
	return result, nil, nil
}
