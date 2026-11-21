package admin

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"golang.org/x/sync/singleflight"
)

// Cache only aggregated admin reports, never usage rows or credentials. The
// original observation time remains visible. Eight entries, each <= 4 MiB.
type profitReportCache struct {
	mu         sync.Mutex
	generation uint64
	slots      chan struct{}
	entries    map[service.UpstreamOperationsFilter]profitReportEntry
	flight     singleflight.Group
}
type profitReportEntry struct {
	raw     []byte
	expires time.Time
}

func (c *profitReportCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.entries = nil
}
func (c *profitReportCache) read(ctx context.Context, f service.UpstreamOperationsFilter, load func(context.Context, bool) (*service.ProfitOperationsSnapshot, error)) (*service.ProfitOperationsSnapshot, error) {
	report, err := c.readOnce(ctx, f, load)
	// A refresh may join the previous request while its canceled query is still
	// unwinding. singleflight removes that flight before delivering its error,
	// so a live waiter can safely join/start a fresh flight. Retry only once;
	// deadlines and ordinary query failures keep their existing behavior.
	if errors.Is(err, context.Canceled) && ctx.Err() == nil {
		return c.readOnce(ctx, f, load)
	}
	return report, err
}

func (c *profitReportCache) readOnce(ctx context.Context, f service.UpstreamOperationsFilter, load func(context.Context, bool) (*service.ProfitOperationsSnapshot, error)) (*service.ProfitOperationsSnapshot, error) {
	key := f
	key.Page = 1
	key.PageSize = 20
	key.Sort = "requests"
	key.Direction = "desc"
	key.From = key.From.UTC()
	key.Until = key.Until.UTC()
	c.mu.Lock()
	if c.slots == nil {
		c.slots = make(chan struct{}, 2)
	}
	slots := c.slots
	generation := c.generation
	entry, ok := c.entries[key]
	c.mu.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return pageProfitReport(entry.raw, f)
	}
	encoded, _ := json.Marshal(struct {
		Filter     service.UpstreamOperationsFilter
		Generation uint64
	}{key, generation})
	result := c.flight.DoChan(string(encoded), func() (any, error) {
		// A leader's cancellation also cancels its query; failures are never cached.
		c.mu.Lock()
		entry, ok := c.entries[key]
		c.mu.Unlock()
		if ok && time.Now().Before(entry.expires) {
			return entry.raw, nil
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-slots }()
		report, err := load(ctx, true)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(report)
		if err != nil {
			return nil, err
		}
		if len(raw) > 4<<20 || !profitReportFits(report) {
			return []byte(nil), nil
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if generation == c.generation {
			if c.entries == nil {
				c.entries = make(map[service.UpstreamOperationsFilter]profitReportEntry)
			}
			if len(c.entries) >= 8 {
				var oldest service.UpstreamOperationsFilter
				var expires time.Time
				for k, v := range c.entries {
					if expires.IsZero() || v.expires.Before(expires) {
						oldest = k
						expires = v.expires
					}
				}
				delete(c.entries, oldest)
			}
			c.entries[key] = profitReportEntry{raw: raw, expires: time.Now().Add(2 * time.Minute)}
		}
		return raw, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		raw, ok := r.Val.([]byte)
		if !ok {
			return nil, errors.New("invalid profit report cache result")
		}
		if raw == nil {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			defer func() { <-slots }()
			return load(ctx, false)
		}
		return pageProfitReport(raw, f)
	}
}
func profitReportFits(r *service.ProfitOperationsSnapshot) bool {
	var usage map[string]json.RawMessage
	if json.Unmarshal(r.Usage, &usage) != nil {
		return false
	}
	for _, key := range []string{"total", "member_total", "catalog_total"} {
		var n int
		if len(usage[key]) > 0 && (json.Unmarshal(usage[key], &n) != nil || n > 10000) {
			return false
		}
	}
	if r.Quality.Status == "ready" {
		var q struct{ Total int }
		if json.Unmarshal(r.Quality.Data, &q) != nil || q.Total > 10000 {
			return false
		}
	}
	return true
}
func pageProfitReport(raw []byte, f service.UpstreamOperationsFilter) (*service.ProfitOperationsSnapshot, error) {
	var out service.ProfitOperationsSnapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out.Scope = f
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(out.Usage, &usage); err != nil {
		return nil, err
	}
	for _, field := range []string{"rows", "members", "catalog"} {
		if len(usage[field]) == 0 {
			continue
		}
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(usage[field], &rows); err != nil {
			return nil, err
		}
		if field == "rows" {
			sort.SliceStable(rows, func(i, j int) bool {
				a, b := rows[i][f.Sort], rows[j][f.Sort]
				an, bn := len(a) == 0 || string(a) == "null", len(b) == 0 || string(b) == "null"
				if an != bn {
					return !an
				}
				cmp := 0
				if !an {
					if f.Sort == "name" {
						var x, y string
						_ = json.Unmarshal(a, &x)
						_ = json.Unmarshal(b, &y)
						cmp = strings.Compare(x, y)
					} else {
						x, xok := new(big.Rat).SetString(string(a))
						y, yok := new(big.Rat).SetString(string(b))
						if xok && yok {
							cmp = x.Cmp(y)
						}
					}
				}
				if cmp == 0 {
					return strings.Compare(string(rows[i]["key"]), string(rows[j]["key"])) < 0
				}
				if f.Direction == "desc" {
					return cmp > 0
				}
				return cmp < 0
			})
		}
		start := min((f.Page-1)*f.PageSize, len(rows))
		end := min(start+f.PageSize, len(rows))
		usage[field], _ = json.Marshal(rows[start:end])
	}
	out.Usage, _ = json.Marshal(usage)
	if out.Quality.Status == "ready" {
		var quality map[string]json.RawMessage
		if err := json.Unmarshal(out.Quality.Data, &quality); err != nil {
			return nil, err
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(quality["rows"], &rows); err != nil {
			return nil, err
		}
		start := min((f.Page-1)*f.PageSize, len(rows))
		quality["rows"], _ = json.Marshal(rows[start:min(start+f.PageSize, len(rows))])
		out.Quality.Data, _ = json.Marshal(quality)
	}
	return &out, nil
}
