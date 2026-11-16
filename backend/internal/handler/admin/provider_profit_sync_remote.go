package admin

import (
	"context"
	"encoding/json"
	"errors"
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
	if err != nil || d.Sign() <= 0 || d.Exponent() < -64 || d.Exponent() > 32 {
		return "", false
	}
	return d.String(), true
}

func profitSuccess(env map[string]any) bool {
	code, ok := profitInt(env["code"])
	return ok && code == 0
}

func readProviderProfitUsage(ctx context.Context, s *providerSession, w service.ProviderProfitWindow) ([]service.ProviderProfitRemoteCharge, error) {
	if s.record.SiteType != "sub2api" || !w.Valid() {
		return nil, errProviderResponse
	}
	me, env, err := s.call(ctx, http.MethodGet, "/api/v1/auth/me", nil, nil, true)
	if err != nil {
		return nil, err
	}
	profile, _ := me.(map[string]any)
	user, ok := profitInt(profile["id"])
	if !ok || user <= 0 || !profitSuccess(env) {
		return nil, errProviderResponse
	}
	if mode, exists := profile["run_mode"]; exists && mode != "standard" {
		if mode == "simple" {
			return nil, errProviderProfitSimpleMode
		}
		return nil, errProviderResponse
	}
	// The upstream understands inclusive calendar dates, not timestamps. Read
	// all rows for the covering UTC days, then enforce the exact half-open range.
	const size = 100
	first := w.From.UTC().Truncate(24 * time.Hour)
	last := w.Until.UTC().Add(-time.Nanosecond).Truncate(24 * time.Hour)
	q := url.Values{"timezone": {"UTC"}, "start_date": {first.Format(time.DateOnly)}, "end_date": {last.Format(time.DateOnly)}, "sort_by": {"id"}, "sort_order": {"asc"}, "page_size": {"100"}}
	type identity struct {
		key     int64
		request string
	}
	seen := make(map[identity]int)
	var candidates []service.ProviderProfitRemoteCharge
	var previousID int64
	total := -1
	for page := 1; ; page++ {
		q.Set("page", strconv.Itoa(page))
		data, env, err := s.call(ctx, http.MethodGet, "/api/v1/usage?"+q.Encode(), nil, nil, true)
		if err != nil {
			return nil, err
		}
		if !profitSuccess(env) {
			return nil, errProviderResponse
		}
		object, _ := data.(map[string]any)
		rows, ok := object["items"].([]any)
		count, cOK := profitInt(object["total"])
		current, pOK := profitInt(object["page"])
		batch, bOK := profitInt(object["page_size"])
		if !ok || !cOK || count < 0 || !pOK || current != int64(page) || !bOK || batch != size {
			return nil, errProviderResponse
		}
		if count > service.ProviderProfitMaxRows {
			return nil, service.ErrProviderProfitSyncLimit
		}
		if pages, exists := object["pages"]; exists {
			lastPage, ok := profitInt(pages)
			if !ok || lastPage != max(1, (count+size-1)/size) {
				return nil, errProviderResponse
			}
		}
		if total < 0 {
			total = int(count)
		}
		remain := total - (page-1)*size
		if total != int(count) || len(rows) != min(size, remain) {
			return nil, errProviderResponse
		}
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			id, ok := profitInt(row["id"])
			u, userOK := profitInt(row["user_id"])
			key, keyOK := profitInt(row["api_key_id"])
			ts, _ := row["created_at"].(string)
			at, err := time.Parse(time.RFC3339Nano, ts)
			if !ok || id <= previousID || !userOK || u != user || !keyOK || key <= 0 || err != nil || at.Before(first) || !at.Before(last.AddDate(0, 0, 1)) {
				return nil, errProviderResponse
			}
			previousID = id
			req, _ := row["request_id"].(string)
			if !service.ProviderProfitRequestIDUsable(req) {
				continue
			}
			// Include non-billed and calendar over-read rows in ambiguity detection.
			// A request ID on a different key is NOT the same identity.
			k := identity{key, req}
			seen[k]++
			// PostgreSQL evidence is microsecond-precise. Do not round an
			// upstream instant across a provenance or window boundary.
			if seen[k] != 1 || !w.Contains(at) || at.Nanosecond()%1000 != 0 {
				continue
			}
			billing, ok := profitInt(row["billing_type"])
			if !ok || billing != int64(service.BillingTypeBalance) || row["subscription_id"] != nil {
				continue
			}
			amount, ok := profitDecimal(row["actual_cost"])
			if !ok {
				continue
			}
			candidates = append(candidates, service.ProviderProfitRemoteCharge{UsageID: id, UserID: user, KeyID: key, RequestID: req, CreatedAt: at, Amount: amount})
		}
		if remain <= size {
			break
		}
	}
	unique := candidates[:0]
	for _, r := range candidates {
		if seen[identity{r.KeyID, r.RequestID}] == 1 {
			unique = append(unique, r)
		}
	}
	if err := readProviderProfitProofs(ctx, s, unique); err != nil {
		return nil, err
	}
	return unique, nil
}
