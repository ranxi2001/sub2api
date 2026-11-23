package handler

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	ratelimit "github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type UsageRecordsHandler struct {
	usage        *UsageHandler
	limiter      *ratelimit.RateLimiter
	cursorSecret []byte
}

func NewUsageRecordsHandler(usage *UsageHandler, limiter *ratelimit.RateLimiter, cfg *config.Config) *UsageRecordsHandler {
	return &UsageRecordsHandler{usage: usage, limiter: limiter, cursorSecret: []byte(cfg.JWT.Secret)}
}

// Only fields explicitly selected from the end-user DTO leave this endpoint.
type usageRecord struct {
	ID                    int64     `json:"id"`
	RequestID             string    `json:"request_id"`
	CreatedAt             time.Time `json:"created_at"`
	Model                 string    `json:"model"`
	UpstreamResponseModel *string   `json:"upstream_response_model,omitempty"`
	ReasoningEffort       *string   `json:"reasoning_effort,omitempty"`
	InboundEndpoint       *string   `json:"inbound_endpoint,omitempty"`
	IPAddress             *string   `json:"ip_address,omitempty"`
	RequestType           string    `json:"request_type"`
	Stream                bool      `json:"stream"`
	BillingMode           *string   `json:"billing_mode,omitempty"`
	InputTokens           int       `json:"input_tokens"`
	OutputTokens          int       `json:"output_tokens"`
	CacheReadTokens       int       `json:"cache_read_tokens"`
	CacheCreationTokens   int       `json:"cache_creation_tokens"`
	ImageCount            int       `json:"image_count"`
	TotalCost             float64   `json:"total_cost"`
	ActualCost            float64   `json:"actual_cost"`
	RateMultiplier        float64   `json:"rate_multiplier"`
	DurationMs            *int      `json:"duration_ms"`
	FirstTokenMs          *int      `json:"first_token_ms"`
}

func newUsageRecord(l *dto.UsageLog) usageRecord {
	return usageRecord{
		ID: l.ID, RequestID: l.RequestID, CreatedAt: l.CreatedAt.UTC(),
		Model: l.Model, UpstreamResponseModel: l.UpstreamResponseModel, ReasoningEffort: l.ReasoningEffort,
		InboundEndpoint: l.InboundEndpoint, IPAddress: l.IPAddress, RequestType: l.RequestType, Stream: l.Stream,
		BillingMode: l.BillingMode, InputTokens: l.InputTokens, OutputTokens: l.OutputTokens,
		CacheReadTokens: l.CacheReadTokens, CacheCreationTokens: l.CacheCreationTokens, ImageCount: l.ImageCount,
		TotalCost: l.TotalCost, ActualCost: l.ActualCost, RateMultiplier: l.RateMultiplier,
		DurationMs: l.DurationMs, FirstTokenMs: l.FirstTokenMs,
	}
}

func usageRecordsError(c *gin.Context, status int, kind, message string) {
	c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": kind, "message": message}})
}

func (h *UsageRecordsHandler) List(c *gin.Context) {
	key, keyOK := middleware.GetAPIKeyFromContext(c)
	subject, userOK := middleware.GetAuthSubjectFromContext(c)
	if !keyOK || key == nil || !userOK || subject.UserID <= 0 || key.ID <= 0 || key.UserID != subject.UserID {
		usageRecordsError(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	if !h.allow(c, key.ID, subject.UserID) {
		return
	}

	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		usageRecordsError(c, http.StatusBadRequest, "invalid_request_error", "Malformed query string")
		return
	}
	q, err := parseUsageRecordsQuery(values, time.Now())
	if err != nil {
		usageRecordsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if values.Has("cursor") && !h.resumeCursor(&q, values.Get("cursor"), subject.UserID, key.ID) {
		usageRecordsError(c, http.StatusBadRequest, "invalid_request_error", "Invalid cursor for this key, time range or format")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	showModel := false
	showCacheRate := false
	if h.usage.settingService != nil {
		showModel = h.usage.settingService.IsUserUpstreamResponseModelVisible(ctx)
		showCacheRate = h.usage.settingService.IsUserUsageCacheRateVisible(ctx)
	}
	if q.format == "csv" {
		current := usageCSVColumns{Model: showModel, CacheRate: showCacheRate}
		if q.columns == nil {
			q.columns = &current
		} else if (q.columns.Model && !current.Model) || (q.columns.CacheRate && !current.CacheRate) {
			usageRecordsError(c, http.StatusConflict, "invalid_request_error", "CSV column visibility changed. Restart from the first page without a cursor.")
			return
		}
		showModel = q.columns.Model
	}
	var logs []service.UsageLog
	if q.start.Before(q.end) {
		logs, err = h.usage.usageService.ListUsageRecords(ctx, subject.UserID, key.ID, q.start, q.end, q.after, q.limit)
		if err != nil {
			slog.WarnContext(ctx, "list usage records failed", "error", err)
			usageRecordsError(c, http.StatusServiceUnavailable, "api_error", "Usage records temporarily unavailable")
			return
		}
	}
	hasMore := len(logs) > q.limit
	if hasMore {
		logs = logs[:q.limit]
	}
	data := make([]usageRecord, 0, len(logs))
	for i := range logs {
		data = append(data, newUsageRecord(h.usage.userUsageLogDTO(c, &logs[i], showModel)))
	}
	var next *string
	if hasMore {
		token, err := h.nextCursor(q, data[len(data)-1], subject.UserID, key.ID)
		if err != nil {
			usageRecordsError(c, http.StatusInternalServerError, "api_error", "Failed to encode usage cursor")
			return
		}
		next = &token
	}
	if q.format == "csv" {
		c.Header("X-Has-More", strconv.FormatBool(hasMore))
		if next != nil {
			c.Header("X-Next-Cursor", *next)
		}
		c.Header("Content-Disposition", `attachment; filename="usage-records.csv"`)
		c.Data(http.StatusOK, "text/csv; charset=utf-8", usageRecordsCSV(data, key.Name, showModel, q.columns.CacheRate, c.GetHeader("Accept-Language")))
		return
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data, "has_more": hasMore, "next_cursor": next, "start_time": q.start, "end_time": q.end})
}

func (h *UsageRecordsHandler) allow(c *gin.Context, keyID, userID int64) bool {
	ctx := c.Request.Context()
	bucket := "usage-records:{u:" + strconv.FormatInt(userID, 10) + "}:"
	budget, err := h.limiter.AllowTogether(ctx,
		bucket+"key:"+strconv.FormatInt(keyID, 10), 60,
		bucket+"user", 120,
		time.Minute,
	)
	if err != nil {
		slog.WarnContext(ctx, "usage records rate limiter unavailable", "error", err)
		usageRecordsError(c, http.StatusServiceUnavailable, "api_error", "Usage records temporarily unavailable")
		return false
	}
	if !budget.Allowed {
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
		retryAfter := int(math.Ceil(budget.RetryAfter.Seconds()))
		if retryAfter < 1 {
			retryAfter = 1
		}
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		if budget.BlockedScope == "user" {
			usageRecordsError(c, http.StatusTooManyRequests, "rate_limit_error", "Usage records limited to 120 requests per minute per user across all API keys")
		} else {
			usageRecordsError(c, http.StatusTooManyRequests, "rate_limit_error", "Usage records limited to 60 requests per minute per API key")
		}
		return false
	}
	return true
}
