//go:build unit

package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIVideoBindings struct {
	grokMediaSlotBindings
	tasks       map[string]int64
	failPending bool
}

func (b *openAIVideoBindings) GetSessionAccountID(_ context.Context, groupID int64, key string) (int64, error) {
	return b.tasks[fmt.Sprintf("%d:%s", groupID, key)], nil
}

func (b *openAIVideoBindings) SetSessionAccountID(_ context.Context, groupID int64, key string, owner int64, _ time.Duration) error {
	b.tasks[fmt.Sprintf("%d:%s", groupID, key)] = owner
	return nil
}

func (b *openAIVideoBindings) SetGrokVideoPendingBilling(ctx context.Context, key string, body []byte, ttl time.Duration) error {
	if b.failPending {
		return fmt.Errorf("test task store unavailable")
	}
	return b.grokMediaSlotBindings.SetGrokVideoPendingBilling(ctx, key, body, ttl)
}

func TestOpenAIVideoLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(24)
	unitPrice := 0.25
	group := &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true,
		RateMultiplier: 1, VideoPrice720P: &unitPrice}
	accounts := []service.Account{}
	for i, model := range []string{"video-a", "video-b", "text-only"} {
		capabilities := []string{"chat_completions"}
		if i < 2 {
			capabilities = []string{"videos"}
		}
		accounts = append(accounts, service.Account{
			ID: int64(i + 1), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID}, Concurrency: 5,
			Credentials: map[string]any{
				"base_url": "https://relay-" + model + ".example/v1", "api_key": "key-" + model,
				"openai_capabilities": capabilities,
				"model_mapping":       map[string]any{model: model + "-upstream"},
			},
		})
	}
	bindings := &openAIVideoBindings{tasks: map[string]int64{}}
	upstream := &grokMediaSlotUpstream{call: func(req *http.Request, accountID int64) (*http.Response, error) {
		require.LessOrEqual(t, accountID, int64(2))
		model := []string{"video-a", "video-b"}[int(accountID)-1]
		require.Equal(t, "relay-"+model+".example", req.URL.Host)
		require.Equal(t, "Bearer key-"+model, req.Header.Get("Authorization"))
		require.True(t, service.HTTPUpstreamRedirectsDisabled(req.Context()))
		response := func(code int, contentType, body string) *http.Response {
			return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {contentType}},
				Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
		}
		switch {
		case req.Method == http.MethodPost:
			require.Equal(t, "/v1/videos", req.URL.Path)
			if accountID == 1 {
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Equal(t, model+"-upstream", gjson.GetBytes(body, "model").String())
				require.Equal(t, "keep", gjson.GetBytes(body, "metadata.passthrough").String())
				return response(http.StatusAccepted, "application/json", `{"id":"same-task","status":"queued"}`), nil
			}
			require.NoError(t, req.ParseMultipartForm(1<<16))
			require.Equal(t, model+"-upstream", req.FormValue("model"))
			file, header, err := req.FormFile("input_reference")
			require.NoError(t, err)
			defer file.Close()
			require.Equal(t, "ref.png", header.Filename)
			contents, err := io.ReadAll(file)
			require.NoError(t, err)
			require.Equal(t, "reference", string(contents))
			return response(http.StatusOK, "application/json", `{"id":"same-task","status":"queued"}`), nil
		case req.URL.Path == "/v1/videos/same-task/content":
			require.Equal(t, "bytes=0-", req.Header.Get("Range"))
			return response(http.StatusPartialContent, "video/mp4", "video-from-"+model), nil
		default:
			require.Equal(t, "/v1/videos/same-task", req.URL.Path)
			return response(http.StatusOK, "application/json", `{"id":"same-task","status":"completed","video_url":"/v1/videos/same-task/content"}`), nil
		}
	}}

	slots := &grokMediaSlotsCache{accounts: map[string]int64{}, users: map[string]int64{}}
	concurrency := service.NewConcurrencyService(slots)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	usage := &cachedBillingUsageRepo{}
	gateway := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: accounts}, nil, usage, nil,
		nil, nil, nil, bindings, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billingCache, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billingCache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		id, err := strconv.ParseInt(c.GetHeader("X-Test-Key"), 10, 64)
		require.NoError(t, err)
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: id, UserID: 10, GroupID: &groupID, Group: group, User: &service.User{ID: 10}})
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 10, Concurrency: 5})
		c.Next()
	})
	router.POST("/v1/videos", func(c *gin.Context) { require.True(t, h.TryOpenAIVideoGeneration(c)) })
	router.GET("/v1/videos/:request_id", h.OpenAIVideoLookup)
	router.GET("/v1/videos/:request_id/content", h.OpenAIVideoLookup)
	request := func(method, path, contentType string, body io.Reader, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("X-Test-Key", key)
		req.Header.Set("Range", "bytes=0-")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	for _, body := range []string{
		`{"model":"video-a","seconds":5,"prompt":"waves","prompt":"other"}`,
		`{"model":"video-a","seconds":5,"Prompt":"other"}`,
		`{"model":"video-a","seconds":5,"input_reference":{"image_url":"a","image_url":"b"}}`,
		`{"model":"video-a","seconds":5,"metadata":{"content":[],"content":[{"image_url":"b"}]}}`,
	} {
		rejected := request(http.MethodPost, "/v1/videos", "application/json", strings.NewReader(body), "20")
		require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
		require.Equal(t, "invalid_request_error", gjson.GetBytes(rejected.Body.Bytes(), "error.type").String())
		require.Zero(t, upstream.calls, "ambiguous moderation input must not reach the upstream")
	}
	var ambiguousForm bytes.Buffer
	ambiguousMP := multipart.NewWriter(&ambiguousForm)
	require.NoError(t, ambiguousMP.WriteField("model", "video-a"))
	require.NoError(t, ambiguousMP.WriteField("seconds", "5"))
	require.NoError(t, ambiguousMP.WriteField("prompt", "waves"))
	require.NoError(t, ambiguousMP.WriteField("prompt", "other"))
	require.NoError(t, ambiguousMP.Close())
	rejected := request(http.MethodPost, "/v1/videos", ambiguousMP.FormDataContentType(), &ambiguousForm, "20")
	require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
	require.Zero(t, upstream.calls, "ambiguous multipart input must not reach the upstream")

	createdA := request(http.MethodPost, "/v1/videos", "application/json", strings.NewReader(`{"model":"video-a","prompt":"test","seconds":30,"size":"1280x720","metadata":{"passthrough":"keep"}}`), "20")
	require.Equal(t, http.StatusAccepted, createdA.Code, createdA.Body.String())
	idA := gjson.GetBytes(createdA.Body.Bytes(), "id").String()
	require.True(t, service.IsOpenAIVideoTaskID(idA))

	var form bytes.Buffer
	mp := multipart.NewWriter(&form)
	require.NoError(t, mp.WriteField("model", "video-b"))
	require.NoError(t, mp.WriteField("prompt", "test"))
	require.NoError(t, mp.WriteField("seconds", "6"))
	require.NoError(t, mp.WriteField("size", "720x1280"))
	file, err := mp.CreateFormFile("input_reference", "ref.png")
	require.NoError(t, err)
	_, err = io.WriteString(file, "reference")
	require.NoError(t, err)
	require.NoError(t, mp.Close())
	createdB := request(http.MethodPost, "/v1/videos", mp.FormDataContentType(), &form, "20")
	require.Equal(t, http.StatusOK, createdB.Code, createdB.Body.String())
	idB := gjson.GetBytes(createdB.Body.Bytes(), "id").String()
	require.True(t, service.IsOpenAIVideoTaskID(idB))
	require.NotEqual(t, idA, idB)
	require.Empty(t, usage.logs, "accepting a task must not bill")
	require.Equal(t, http.StatusNotFound, request(http.MethodGet, "/v1/videos/"+idA, "", nil, "21").Code)
	require.Equal(t, 2, upstream.calls, "a different API key must not reach the upstream")

	// Downloads and completed status are both evidence of completion. Either may arrive first.
	downloadB := request(http.MethodGet, "/v1/videos/"+idB+"/content", "", nil, "20")
	require.Equal(t, http.StatusPartialContent, downloadB.Code)
	require.Equal(t, "video-from-video-b", downloadB.Body.String())
	pollA := request(http.MethodGet, "/v1/videos/"+idA, "", nil, "20")
	require.Equal(t, http.StatusOK, pollA.Code)
	require.Equal(t, idA, gjson.GetBytes(pollA.Body.Bytes(), "id").String())
	require.Equal(t, "/v1/videos/"+idA+"/content", gjson.GetBytes(pollA.Body.Bytes(), "video_url").String())
	downloadA := request(http.MethodGet, "/v1/videos/"+idA+"/content", "", nil, "20")
	require.Equal(t, http.StatusPartialContent, downloadA.Code)
	require.Equal(t, "video-from-video-a", downloadA.Body.String())
	require.Equal(t, http.StatusOK, request(http.MethodGet, "/v1/videos/"+idB, "", nil, "20").Code)
	require.Len(t, usage.logs, 2, "polling and downloading must not charge twice")
	require.Equal(t, []float64{1.5, 7.5}, usage.totals())
	require.Equal(t, 30, *usage.logs[1].VideoDurationSeconds)

	bindings.failPending = true
	failed := request(http.MethodPost, "/v1/videos", "application/json", strings.NewReader(`{"model":"video-a","prompt":"test","seconds":30,"metadata":{"passthrough":"keep"}}`), "20")
	require.Equal(t, http.StatusBadGateway, failed.Code)
	require.Empty(t, gjson.GetBytes(failed.Body.Bytes(), "id").String(), "a task ID must not be returned without its saved context")
	require.Len(t, usage.logs, 2)
	slots.assertReleased(t)
}
