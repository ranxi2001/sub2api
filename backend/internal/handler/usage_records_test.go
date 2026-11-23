//go:build unit

package handler

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	ratelimit "github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/pkg/qualityqueue/queuetest"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type recordsTestRepo struct {
	service.UsageLogRepository
	onRead func(context.Context)
	list   func(int64, int64, time.Time, time.Time, *service.UsageRecordPosition, int) ([]service.UsageLog, error)
}

func (r recordsTestRepo) ListUsageRecords(ctx context.Context, user, key int64, start, end time.Time, pos *service.UsageRecordPosition, limit int) ([]service.UsageLog, error) {
	if r.onRead != nil {
		r.onRead(ctx)
	}
	return r.list(user, key, start, end, pos, limit)
}

type recordsTestSettings struct {
	service.SettingRepository
	values map[string]string
	onRead func(context.Context)
}

func (s recordsTestSettings) GetMultiple(ctx context.Context, _ []string) (map[string]string, error) {
	if s.onRead != nil {
		s.onRead(ctx)
	}
	return s.values, nil
}

type recordsFixture struct {
	t     *testing.T
	h     *UsageRecordsHandler
	redis *miniredis.Miniredis
}

func newRecordsFixture(t *testing.T, repo recordsTestRepo, flags map[string]string) recordsFixture {
	cache := miniredis.RunT(t)
	client := queuetest.NewClient(cache.Addr())
	t.Cleanup(func() { _ = client.Close() })
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "unit-test-cursor-secret"}}
	settings := service.NewSettingService(recordsTestSettings{values: flags, onRead: repo.onRead}, cfg)
	usage := NewUsageHandler(service.NewUsageService(repo, nil, nil, nil), nil, nil, settings)
	return recordsFixture{t, NewUsageRecordsHandler(usage, ratelimit.NewRateLimiter(client), cfg), cache}
}

func TestUsageRecordsCSVReadBudget(t *testing.T) {
	var deadlines []time.Time
	f := newRecordsFixture(t, recordsTestRepo{
		onRead: func(ctx context.Context) {
			end, ok := ctx.Deadline()
			require.True(t, ok)
			deadlines = append(deadlines, end)
		},
		list: func(_, _ int64, _, _ time.Time, _ *service.UsageRecordPosition, _ int) ([]service.UsageLog, error) {
			return nil, nil
		},
	}, nil)
	before := time.Now()
	require.Equal(t, 200, f.get("start_time=0&end_time=1&format=csv", 7, 5).Code)
	require.Len(t, deadlines, 3)
	require.Equal(t, deadlines[0], deadlines[1])
	require.Equal(t, deadlines[0], deadlines[2])
	require.False(t, deadlines[0].Before(before.Add(5*time.Second)))
	require.False(t, deadlines[0].After(time.Now().Add(5*time.Second)))
}

func (f recordsFixture) get(query string, user, key int64) *httptest.ResponseRecorder {
	f.t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage/records?"+query, nil)
	c.Request.Header.Set("Accept-Language", "en")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: key, UserID: user, Name: "=private,name"})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user})
	f.h.List(c)
	return w
}

func TestUsageRecordsValidation(t *testing.T) {
	f := newRecordsFixture(t, recordsTestRepo{}, nil)
	for _, q := range []string{"", "start_time=1", "end_time=2",
		"start_time=0&end_time=604801", "start_time=1&end_time=1", "start_time=2&end_time=1", "start_time=1&end_time=xyz",
		"start_time=2025-01-01T00:00:00%2B24:00&end_time=2025-01-02T00:00:00Z",
		"start_time=1&end_time=2&cursor=broken", "start_time=1&end_time=2&cursor=", "start_time=1&end_time=2&cursor=%zz", "start_time=1&end_time=2&format=x",
		"start_time=1&end_time=2&limit=0", "start_time=1&end_time=2&limit=1001", "start_time=1&end_time=2&limit=abc"} {
		t.Run(q, func(t *testing.T) {
			w := f.get(q, 7, 5)
			require.Equal(t, 400, w.Code)
			require.Contains(t, w.Body.String(), `"type":"invalid_request_error"`)
		})
	}
	for _, param := range []string{"start_time", "end_time"} {
		for _, invalid := range []string{"2025-01-01T01:00:00", "2025-01-01T01:00:00.0000000001Z"} {
			values := url.Values{"start_time": {"2025-01-01T00:00:00Z"}, "end_time": {"2025-01-01T02:00:00Z"}}
			values.Set(param, invalid)
			w := f.get(values.Encode(), 7, 5)
			require.Equal(t, 400, w.Code)
			require.Contains(t, w.Body.String(), `"message":"`+param+` requires`)
		}
	}
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	v := url.Values{"start_time": {"2025-01-01T08:00:00+08:00"}, "end_time": {"2025-01-09T01:00:00Z"}}
	q, err := parseUsageRecordsQuery(v, start.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 500, q.limit)
	require.Equal(t, "json", q.format)
	require.Equal(t, start, q.start)
	require.Equal(t, start.Add(time.Hour), q.end)
	w := f.get("start_time=2099-01-01T00:00:00Z&end_time=2099-01-12T00:00:00Z", 7, 5)
	var empty struct {
		Start time.Time     `json:"start_time"`
		End   time.Time     `json:"end_time"`
		Data  []usageRecord `json:"data"`
	}
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &empty))
	require.False(t, empty.Start.IsZero())
	require.Equal(t, empty.Start, empty.End)
	require.Empty(t, empty.Data)
}

func TestUsageRecordsPagesAndVisibility(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	stamp := start.Add(time.Minute + 123456*time.Microsecond)
	requested, upstream, effort := "public-model", "private-model", "low"
	newRow := func(id int64) service.UsageLog {
		return service.UsageLog{ID: id, CreatedAt: stamp, RequestID: "client:test", Model: upstream, RequestedModel: requested,
			UpstreamResponseModel: &upstream, ReasoningEffort: &upstream, RequestedReasoningEffort: &effort,
			AccountID: 55, UpstreamModel: &upstream, UpstreamRequestID: &upstream, AccountStatsCost: new(float64),
			Stream: true, TotalCost: 2, ActualCost: 1, RateMultiplier: 0.5}
	}
	var firstEnd time.Time
	repo := recordsTestRepo{list: func(user, key int64, from, to time.Time, pos *service.UsageRecordPosition, limit int) ([]service.UsageLog, error) {
		require.Equal(t, int64(7), user)
		require.Equal(t, int64(5), key)
		require.Equal(t, 1, limit)
		require.True(t, start.Equal(from))
		if pos == nil {
			firstEnd = to
			return []service.UsageLog{newRow(10), newRow(11)}, nil
		}
		require.True(t, firstEnd.Equal(to))
		require.Equal(t, int64(10), pos.ID)
		require.True(t, stamp.Equal(pos.CreatedAt))
		return []service.UsageLog{newRow(11)}, nil
	}}
	f := newRecordsFixture(t, repo, nil)
	query := "start_time=" + strconv.FormatInt(start.Unix(), 10) + "&end_time=" + strconv.FormatInt(start.Add(2*time.Hour).Unix(), 10) + "&limit=1"
	w := f.get(query, 7, 5)
	require.Equal(t, http.StatusOK, w.Code)
	var page struct {
		Object string
		Data   []map[string]any
		More   bool      `json:"has_more"`
		Cursor *string   `json:"next_cursor"`
		End    time.Time `json:"end_time"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.Equal(t, "list", page.Object)
	require.True(t, page.More)
	require.True(t, firstEnd.Equal(page.End))
	require.Len(t, page.Data, 1)
	row := page.Data[0]
	fields := []string{"id", "request_id", "created_at", "model", "request_type", "stream", "reasoning_effort", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "image_count", "actual_cost", "total_cost", "rate_multiplier", "duration_ms", "first_token_ms"}
	require.Len(t, row, len(fields))
	for _, k := range fields {
		require.Contains(t, row, k)
	}
	require.Equal(t, "public-model", row["model"])
	require.Equal(t, "low", row["reasoning_effort"])
	require.NotContains(t, w.Body.String(), upstream)
	require.NotNil(t, page.Cursor)
	nextQuery := query + "&cursor=" + *page.Cursor
	for _, owner := range [][2]int64{{7, 6}, {8, 5}} {
		require.Equal(t, 400, f.get(nextQuery, owner[0], owner[1]).Code)
	}
	wire, err := base64.RawURLEncoding.DecodeString(*page.Cursor)
	require.NoError(t, err)
	wire[len(wire)-1] ^= 1
	require.Equal(t, 400, f.get(query+"&cursor="+base64.RawURLEncoding.EncodeToString(wire), 7, 5).Code)
	w = f.get(nextQuery, 7, 5)
	require.Equal(t, 200, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.False(t, page.More)
	require.Nil(t, page.Cursor)
	require.Equal(t, float64(11), page.Data[0]["id"])
}

func TestUsageRecordsCSV(t *testing.T) {
	model, ip := "response", "192.0.2.1"
	repo := recordsTestRepo{list: func(_, _ int64, start, _ time.Time, pos *service.UsageRecordPosition, _ int) ([]service.UsageLog, error) {
		log := service.UsageLog{ID: 10, CreatedAt: start, Model: "public", UpstreamResponseModel: &model, IPAddress: &ip,
			InputTokens: 3, CacheReadTokens: 1, ActualCost: 0.001953125, TotalCost: 0.25, RateMultiplier: 0.5}
		if pos != nil {
			log.ID++
			return []service.UsageLog{log}, nil
		}
		return []service.UsageLog{log, log}, nil
	}}
	for _, visible := range []bool{false, true} {
		t.Run(strconv.FormatBool(visible), func(t *testing.T) {
			flags := map[string]string{
				service.SettingKeyUsageShowCacheRateToUsers:             strconv.FormatBool(visible),
				service.SettingKeyUsageShowUpstreamResponseModelToUsers: strconv.FormatBool(visible),
			}
			f := newRecordsFixture(t, repo, flags)
			query := "start_time=0&end_time=10&limit=1&format=csv"
			w := f.get(query, 7, 5)
			require.Equal(t, 200, w.Code)
			require.Equal(t, "text/csv; charset=utf-8", w.Header().Get("Content-Type"))
			require.Equal(t, "true", w.Header().Get("X-Has-More"))
			require.True(t, strings.HasPrefix(w.Body.String(), "\uFEFF"))
			cells, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\uFEFF"))).ReadAll()
			require.NoError(t, err)
			require.Len(t, cells, 2)
			header := "Time|API Key Name|Model|Reasoning Effort|Inbound Endpoint|IP Address|Type|Billing Mode|Input Tokens|Output Tokens|Cache Read Tokens|Cache Creation Tokens|Rate Multiplier|Billed Cost|Original Cost|First Token (ms)|Duration (ms)"
			row := "1970-01-01T00:00:00Z|'=private,name|public|-||192.0.2.1|Sync|Token|3|0|1|0|0.5|0.00195313|0.25000000||"
			if visible {
				header = strings.Replace(header, "Model|", "Model|Upstream Response Model|", 1)
				header = strings.Replace(header, "|Rate Multiplier", "|Cache Rate|Rate Multiplier", 1)
				row = strings.Replace(row, "public|", "public|response|", 1)
				row = strings.Replace(row, "|0.5|", "|25.0%|0.5|", 1)
			}
			require.Equal(t, strings.Split(header, "|"), cells[0])
			require.Equal(t, strings.Split(row, "|"), cells[1])
			next := query + "&cursor=" + w.Header().Get("X-Next-Cursor")
			for _, flag := range []string{service.SettingKeyUsageShowUpstreamResponseModelToUsers, service.SettingKeyUsageShowCacheRateToUsers} {
				flags[flag] = strconv.FormatBool(!visible)
				w = f.get(next, 7, 5)
				if visible {
					require.Equal(t, 409, w.Code)
					require.Contains(t, w.Body.String(), "Restart from the first page")
					require.NotContains(t, w.Body.String(), "response")
					require.Empty(t, w.Header().Get("X-Next-Cursor"))
				} else {
					require.Equal(t, 200, w.Code)
					require.Equal(t, strings.Split(w.Body.String(), "\n")[0], "\uFEFF"+strings.ReplaceAll(header, "|", ","))
				}
				flags[flag] = strconv.FormatBool(visible)
			}
			w = f.get(next, 7, 5)
			require.Equal(t, "false", w.Header().Get("X-Has-More"))
			require.Empty(t, w.Header().Get("X-Next-Cursor"))
		})
	}
}

func TestUsageRecordsCSVBillingModeLabelsMatchUserExport(t *testing.T) {
	// Source of truth: frontend/src/i18n/locales/zh/admin/resources.ts,
	// admin.usage.billingMode{Token,PerRequest,Image,Video}.
	for _, tc := range []struct {
		mode, want string
	}{
		{mode: "token", want: "按量"},
		{mode: "per_request", want: "按次"},
		{mode: "image", want: "按次(图片)"},
		{mode: "video", want: "按次(视频)"},
	} {
		row := usageRecord{BillingMode: &tc.mode}
		require.Equal(t, tc.want, usageCSVBillingMode(row, true), tc.mode)
	}
}

func TestUsageRecordsRateLimitsOps(t *testing.T) {
	for _, perUser := range []bool{false, true} {
		t.Run("perUser="+strconv.FormatBool(perUser), func(t *testing.T) {
			f := newRecordsFixture(t, recordsTestRepo{}, nil)
			setupOpsErrorLogTestQueue(t, 32)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)))
			router.GET("/v1/usage/records", func(c *gin.Context) {
				id, err := strconv.ParseInt(c.GetHeader("X-Test-Key"), 10, 64)
				require.NoError(t, err)
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: id, UserID: 7})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
				f.h.List(c)
			})
			count, maxAllowed, scope := 61, 60, "per API key"
			if perUser {
				count, maxAllowed, scope = 150, 120, "per user across all API keys"
			}
			for i := range count {
				key := 1
				if perUser {
					key += i % 3
				}
				req := httptest.NewRequest(http.MethodGet, "/v1/usage/records?start_time=2099-01-01T00:00:00Z&end_time=2099-01-02T00:00:00Z", nil)
				req.Header.Set("X-Test-Key", strconv.Itoa(key))
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if i < maxAllowed {
					require.Equal(t, 200, w.Code)
					continue
				}
				require.Equal(t, 429, w.Code)
				require.Contains(t, w.Body.String(), scope)
				require.Equal(t, "60", w.Header().Get("Retry-After"))
				select {
				case job := <-opsErrorLogQueue:
					require.True(t, job.entry.IsBusinessLimited)
					require.Equal(t, "auth", job.entry.ErrorPhase)
					require.Equal(t, "client", job.entry.ErrorOwner)
					require.Equal(t, "client_request", job.entry.ErrorSource)
				default:
					t.Fatal("missing Ops error for local 429")
				}
			}
		})
	}
}

func TestUsageRecordsRateLimitAndErrors(t *testing.T) {
	f := newRecordsFixture(t, recordsTestRepo{list: func(_, _ int64, _, _ time.Time, _ *service.UsageRecordPosition, _ int) ([]service.UsageLog, error) {
		return nil, errors.New("private database detail")
	}}, nil)
	// Future windows are empty, while still consuming the per-key budget.
	const query = "start_time=2099-01-01T00:00:00Z&end_time=2099-01-02T00:00:00Z"
	for range 60 {
		require.Equal(t, 200, f.get(query, 7, 5).Code)
	}
	w := f.get(query, 7, 5)
	require.Equal(t, 429, w.Code)
	require.Equal(t, "60", w.Header().Get("Retry-After"))
	require.Equal(t, 200, f.get(query, 7, 6).Code)
	f.redis.FastForward(time.Minute)
	w = f.get("start_time=0&end_time=1", 7, 5)
	require.Equal(t, 503, w.Code)
	require.NotContains(t, w.Body.String(), "private database detail")
	f.redis.Close()
	require.Equal(t, 503, f.get(query, 7, 6).Code)
}

func TestUsageRecordsTwoLevelLimitDoesNotConsumeOtherBucket(t *testing.T) {
	f := newRecordsFixture(t, recordsTestRepo{}, nil)
	const query = "start_time=2099-01-01T00:00:00Z&end_time=2099-01-02T00:00:00Z"
	for range 60 {
		require.Equal(t, http.StatusOK, f.get(query, 7, 1).Code)
	}
	keyLimited := f.get(query, 7, 1)
	require.Equal(t, http.StatusTooManyRequests, keyLimited.Code)
	require.Contains(t, keyLimited.Body.String(), "per API key")
	userCount, err := f.redis.Get("rate_limit:usage-records:{u:7}:user")
	require.NoError(t, err)
	require.Equal(t, "60", userCount)

	for key := int64(2); key <= 4; key++ {
		for range 20 {
			require.Equal(t, http.StatusOK, f.get(query, 7, key).Code)
		}
	}
	userLimited := f.get(query, 7, 5)
	require.Equal(t, http.StatusTooManyRequests, userLimited.Code)
	require.Contains(t, userLimited.Body.String(), "per user across all API keys")
	keyFiveCount, err := f.redis.Get("rate_limit:usage-records:{u:7}:key:5")
	require.Error(t, err)
	require.Empty(t, keyFiveCount)
}

func TestUsageRecordsRateLimitTTLBoundaries(t *testing.T) {
	const query = "start_time=2099-01-01T00:00:00Z&end_time=2099-01-02T00:00:00Z"
	for _, bucket := range []struct {
		key, limit string
	}{
		{"rate_limit:usage-records:{u:7}:key:5", "60"},
		{"rate_limit:usage-records:{u:7}:user", "120"},
	} {
		t.Run(bucket.key+"/PTTL=0", func(t *testing.T) {
			f := newRecordsFixture(t, recordsTestRepo{}, nil)
			require.NoError(t, f.redis.Set(bucket.key, bucket.limit))
			f.redis.SetTTL(bucket.key, 500*time.Microsecond) // PTTL truncates to milliseconds.
			w := f.get(query, 7, 5)
			require.Equal(t, http.StatusTooManyRequests, w.Code)
			require.Equal(t, "1", w.Header().Get("Retry-After"))
			f.redis.FastForward(time.Millisecond)
			require.Equal(t, http.StatusOK, f.get(query, 7, 5).Code)
		})
		t.Run(bucket.key+"/PTTL=-1", func(t *testing.T) {
			f := newRecordsFixture(t, recordsTestRepo{}, nil)
			require.NoError(t, f.redis.Set(bucket.key, bucket.limit)) // Persistent, already full.
			w := f.get(query, 7, 5)
			require.Equal(t, http.StatusTooManyRequests, w.Code)
			require.Equal(t, "60", w.Header().Get("Retry-After"))
			require.Equal(t, time.Minute, f.redis.TTL(bucket.key))
			f.redis.FastForward(59 * time.Second)
			require.Equal(t, "1", f.get(query, 7, 5).Header().Get("Retry-After"))
			f.redis.FastForward(time.Second)
			require.Equal(t, http.StatusOK, f.get(query, 7, 5).Code)
		})
	}
}
