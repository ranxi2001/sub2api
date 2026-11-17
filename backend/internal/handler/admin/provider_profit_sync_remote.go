package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

var errProviderProfitSimpleMode = errors.New("simple mode does not bill the wallet")

func profitInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

// Bound the exponent before expanding to decimal notation. Never round a
// missing, negative or unrepresentable amount into a zero charge.
func profitDecimal(v any) (string, bool) {
	var text string
	switch n := v.(type) {
	case json.Number:
		text = n.String()
	case string:
		text = n
	default:
		return "", false
	}
	if len(text) == 0 || len(text) > 64 {
		return "", false
	}
	d, err := decimal.NewFromString(text)
	if err != nil || d.Sign() < 0 || d.Exponent() < -64 || d.Exponent() > 32 {
		return "", false
	}
	return d.String(), true
}

func profitSuccess(env map[string]any) bool {
	code, ok := profitInt(env["code"])
	return ok && code == 0
}

const (
	// The upstream accepts up to 1000. Each user page does a COUNT, OFFSET
	// and DTO hydration; 250 amortizes that work while leaving ~32 KiB per
	// hydrated row under the session's 8 MiB response limit.
	profitPageSize      = 250
	profitPagesPerBatch = 4
	profitBatchDuration = 4 * time.Second
	// Estimated retained memory, not a limit on total rows from the upstream.
	profitEvidenceBudget = 96 << 20
	// HTTP 429 backoff when the upstream does not send Retry-After.
	profitRateLimitBase = 2 * time.Second
	profitRateLimitCap  = 45 * time.Second
)

var (
	errProviderProfitEvidenceBudget = errors.New("provider usage evidence exceeds the sync memory budget")
	errProfitRateLimited            = errors.New("provider profit sync rate limit exceeded the remaining job time")
)

// Replaced in tests so a 429 retry does not wait on the wall clock.
var (
	profitNow   = time.Now
	profitSleep = func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	profitJitter = func(limit time.Duration) time.Duration {
		if limit <= 0 {
			return 0
		}
		return time.Duration(rand.Int64N(int64(limit)))
	}
)

type profitIdentity struct {
	key     int64
	request string
}

type profitUsageScan struct {
	provider         int64
	window           service.ProviderProfitWindow
	first, end       time.Time // half-open UTC days actually queried
	query            url.Values
	user, previousID int64
	read, total      int64
	retained         int
	// A present false is a singleton, true denotes a duplicate. Both maps
	// include unpriced and outside-window observations on every page.
	byKey      map[profitIdentity]bool
	bySite     map[string]bool
	candidates []service.ProviderProfitRemoteCharge
}

// The caller supplies the job deadline and opens a fresh, bounded, revision-
// pinned session per batch. Never publish intermediate candidates: any later
// page can still disqualify an earlier row, including across session boundaries.
func readProviderProfitUsageBatches(ctx context.Context, providerID int64, w service.ProviderProfitWindow, withSession func(func(context.Context, *providerSession) error) error) ([]service.ProviderProfitRemoteCharge, error) {
	if !w.Valid() {
		return nil, errProviderResponse
	}
	first := w.From.UTC().Truncate(24 * time.Hour)
	last := w.Until.UTC().Add(-time.Nanosecond).Truncate(24 * time.Hour)
	// One scan covers every UTC day in the window. A later page can still
	// disqualify an earlier row, so a finished UTC day is not committed alone.
	scan := profitUsageScan{
		provider: providerID, window: w, first: first, end: last.AddDate(0, 0, 1), total: -1,
		query: url.Values{"timezone": {"UTC"}, "start_date": {first.Format(time.DateOnly)}, "end_date": {last.Format(time.DateOnly)}, "sort_by": {"id"}, "sort_order": {"asc"}},
		byKey: make(map[profitIdentity]bool), bySite: make(map[string]bool),
	}
	for scan.read != scan.total {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := withSession(scan.batch); err != nil {
			return nil, err
		}
	}
	unique := scan.candidates[:0]
	for _, r := range scan.candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !scan.byKey[profitIdentity{r.KeyID, r.RequestID}] {
			r.UniqueAtSite = !scan.bySite[r.RequestID]
			unique = append(unique, r)
		}
	}
	clear(scan.candidates[len(unique):])
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return unique, nil
}

func profitIsRateLimited(err error) bool {
	_, ok := profitRateLimitDelay(err)
	return ok
}

func profitRateLimitDelay(err error) (time.Duration, bool) {
	var remote *providerRemoteError
	if errors.As(err, &remote) && remote.status == http.StatusTooManyRequests {
		return remote.retryAfter, true
	}
	var api *providerAPIError
	if errors.As(err, &api) && api.status == http.StatusTooManyRequests {
		return api.retryAfter, true
	}
	return 0, false
}

func profitBackoff(attempt int, retryAfter time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if retryAfter > 0 {
		return retryAfter
	}
	delay := profitRateLimitBase
	for n := 1; n < attempt; n++ {
		if delay >= profitRateLimitCap {
			return profitRateLimitCap
		}
		delay *= 2
	}
	if delay > profitRateLimitCap {
		delay = profitRateLimitCap
	}
	delay += profitJitter(250 * time.Millisecond)
	if delay > profitRateLimitCap {
		delay = profitRateLimitCap
	}
	return delay
}

func profitWaitFits(ctx context.Context, wait time.Duration) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return deadline.Sub(profitNow()) > wait
}

// profitBackoffWait sleeps after one HTTP 429. A non-429 error is returned
// unchanged. The wait is not logged with the upstream body or any credential.
func profitBackoffWait(ctx context.Context, providerID int64, operation string, attempt int, err error) error {
	retryAfter, limited := profitRateLimitDelay(err)
	if !limited {
		return err
	}
	delay := profitBackoff(attempt, retryAfter)
	fields := []any{"provider_id", providerID, "operation", operation, "attempt", attempt, "wait_ms", delay.Milliseconds(), "retry_after_ms", retryAfter.Milliseconds()}
	if !profitWaitFits(ctx, delay) {
		slog.Warn("provider profit sync rate limit exhausted", append(fields, "reason", "rate_limited")...)
		return errProfitRateLimited
	}
	slog.Warn("provider profit sync rate limited", fields...)
	if sleepErr := profitSleep(ctx, delay); sleepErr != nil {
		if errors.Is(sleepErr, context.DeadlineExceeded) {
			slog.Warn("provider profit sync rate limit exhausted", append(fields, "reason", "rate_limited")...)
			return errProfitRateLimited
		}
		return sleepErr
	}
	return nil
}

func profitRetry(ctx context.Context, providerID int64, operation string, fn func() error) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn()
		if err == nil {
			return nil
		}
		waitErr := profitBackoffWait(ctx, providerID, operation, attempt, err)
		if waitErr != nil {
			return waitErr
		}
	}
}

func (s *profitUsageScan) call(ctx context.Context, session *providerSession, path, operation string) (any, map[string]any, error) {
	var data any
	var env map[string]any
	err := profitRetry(ctx, s.provider, operation, func() error {
		var callErr error
		data, env, callErr = session.call(ctx, http.MethodGet, path, nil, nil, true)
		return callErr
	})
	if err != nil {
		return nil, nil, err
	}
	return data, env, nil
}

func (s *profitUsageScan) batch(ctx context.Context, session *providerSession) error {
	started := time.Now()
	if session.record.SiteType != "sub2api" {
		return errProviderResponse
	}
	me, env, err := s.call(ctx, session, "/api/v1/auth/me", "auth_me")
	if err != nil {
		return err
	}
	profile, _ := me.(map[string]any)
	user, ok := profitInt(profile["id"])
	if !ok || user <= 0 || !profitSuccess(env) || (s.user != 0 && user != s.user) {
		return errProviderResponse
	}
	if mode, exists := profile["run_mode"]; exists && mode != "standard" {
		if mode == "simple" {
			return errProviderProfitSimpleMode
		}
		return errProviderResponse
	}
	s.user = user
	pageBudget := profitPagesPerBatch
	if s.read != 0 {
		// Verify the exact offset we're resuming from, without fetching and
		// hydrating an entire overlapping page. Don't count the anchor twice.
		rows, err := s.page(ctx, session, s.read-1, 1)
		if err != nil {
			return err
		}
		row, _ := rows[0].(map[string]any)
		id, _ := profitInt(row["id"])
		u, _ := profitInt(row["user_id"])
		if id != s.previousID || u != user {
			return errProviderResponse
		}
		pageBudget--
	}
	for n := 0; n < pageBudget; n++ {
		rows, err := s.page(ctx, session, s.read, profitPageSize)
		if err != nil {
			return err
		}
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			if err := s.observe(row); err != nil {
				return err
			}
		}
		s.read += int64(len(rows))
		if s.read == s.total || time.Since(started) >= profitBatchDuration {
			break
		}
	}
	return ctx.Err()
}

func (s *profitUsageScan) page(ctx context.Context, session *providerSession, offset, size int64) ([]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	page := offset/size + 1
	s.query.Set("page", strconv.FormatInt(page, 10))
	s.query.Set("page_size", strconv.FormatInt(size, 10))
	data, env, err := s.call(ctx, session, "/api/v1/usage?"+s.query.Encode(), fmt.Sprintf("usage page=%d size=%d", page, size))
	if err != nil {
		return nil, err
	}
	object, _ := data.(map[string]any)
	rows, ok := object["items"].([]any)
	count, cOK := profitInt(object["total"])
	current, pOK := profitInt(object["page"])
	batch, bOK := profitInt(object["page_size"])
	// These are live OFFSET pages sorted by increasing ID, not a snapshot.
	// Appends leave the prefix unchanged. Follow every newly advertised row
	// to the latest tail; never stop at the first page's count. Shrinkage,
	// overlap or an inconsistent count/list cannot prove a complete bill.
	if !profitSuccess(env) || !ok || !cOK || count < offset || count < s.total || !pOK || current != page || !bOK || batch != size || int64(len(rows)) != min(size, count-offset) {
		return nil, errProviderResponse
	}
	if pages, exists := object["pages"]; exists {
		lastPage, ok := profitInt(pages)
		if !ok || lastPage != 1+(max(1, count)-1)/size {
			return nil, errProviderResponse
		}
	}
	s.total = count
	return rows, nil
}

func (s *profitUsageScan) observe(row map[string]any) error {
	id, ok := profitInt(row["id"])
	u, userOK := profitInt(row["user_id"])
	key, keyOK := profitInt(row["api_key_id"])
	ts, _ := row["created_at"].(string)
	at, err := time.Parse(time.RFC3339Nano, ts)
	if !ok || id <= s.previousID || !userOK || u != s.user || !keyOK || key <= 0 || err != nil || at.Before(s.first) || !at.Before(s.end) {
		return errProviderResponse
	}
	s.previousID = id
	req, _ := row["request_id"].(string)
	if !service.ProviderProfitRequestIDUsable(req) {
		return nil
	}
	k := profitIdentity{key, req}
	if _, exists := s.byKey[k]; exists {
		s.byKey[k], s.bySite[req] = true, true
		return nil
	}
	// Allow for both maps' buckets, growth slack and their string keys.
	// Repeated observations do not grow the index and consume no more budget.
	s.retained += 256 + 2*len(req)
	if s.retained > profitEvidenceBudget {
		return errProviderProfitEvidenceBudget
	}
	s.byKey[k] = false
	_, seen := s.bySite[req]
	s.bySite[req] = seen
	// PostgreSQL evidence is microsecond-precise. Do not round an upstream
	// instant across a provenance or window boundary.
	if !s.window.Contains(at) || at.Nanosecond()%1000 != 0 {
		return nil
	}
	billing, ok := profitInt(row["billing_type"])
	sub, subOK := profitInt(row["subscription_id"])
	balanceBill := billing == int64(service.BillingTypeBalance) && row["subscription_id"] == nil
	subscriptionBill := billing == int64(service.BillingTypeSubscription) && subOK && sub > 0
	if !ok || (!balanceBill && !subscriptionBill) {
		return nil
	}
	amount, ok := profitDecimal(row["actual_cost"])
	if !ok {
		return nil
	}
	// Include the allowed fields retained by the result and slice growth.
	s.retained += 256 + len(req) + len(amount)
	if s.retained > profitEvidenceBudget {
		return errProviderProfitEvidenceBudget
	}
	s.candidates = append(s.candidates, service.ProviderProfitRemoteCharge{UsageID: id, UserID: s.user, KeyID: key, RequestID: req, CreatedAt: at, Amount: amount})
	return nil
}
