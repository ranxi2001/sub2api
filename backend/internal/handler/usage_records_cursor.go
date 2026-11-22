package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type usageRecordsQuery struct {
	start, end, requestedEnd time.Time
	after                    *service.UsageRecordPosition
	limit                    int
	format                   string
	columns                  *usageCSVColumns
}

type usageCSVColumns struct {
	Model     bool `json:"m"`
	CacheRate bool `json:"c"`
}

// time.Parse also accepts a one-digit hour, a decimal comma and +24:00.
var usageRFC3339 = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func usageRecordTime(value string) (time.Time, bool) {
	var t time.Time
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		t = time.Unix(seconds, 0).UTC()
	} else {
		if !usageRFC3339.MatchString(value) {
			return time.Time{}, false
		}
		t, err = time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{}, false
		}
		t = t.UTC()
	}
	return t, t.Year() >= 1 && t.Year() <= 9999
}

func parseUsageRecordsQuery(values url.Values, now time.Time) (usageRecordsQuery, error) {
	q := usageRecordsQuery{limit: 500, format: "json"}
	var ok bool
	if q.start, ok = usageRecordTime(values.Get("start_time")); !ok {
		return q, errors.New("start_time requires Unix seconds or RFC3339 with a timezone and at most 9 fractional digits")
	}
	if q.requestedEnd, ok = usageRecordTime(values.Get("end_time")); !ok {
		return q, errors.New("end_time requires Unix seconds or RFC3339 with a timezone and at most 9 fractional digits")
	}
	if !q.requestedEnd.After(q.start) {
		return q, errors.New("end_time must be later than start_time")
	}
	q.end = q.requestedEnd
	if q.end.After(now) {
		q.end = now.UTC()
	}
	if q.start.After(q.end) {
		q.start = q.end
	}
	// Continuations restore the signed first page's already validated window.
	if !values.Has("cursor") && q.end.Sub(q.start) > 7*24*time.Hour {
		return q, errors.New("the effective time range must not exceed 7 days")
	}
	if values.Has("limit") {
		limit, err := strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 1 || limit > 1000 {
			return q, errors.New("limit must be an integer from 1 to 1000")
		}
		q.limit = limit
	}
	if values.Has("format") {
		q.format = values.Get("format")
		if q.format != "json" && q.format != "csv" {
			return q, errors.New("format must be json or csv")
		}
	}
	return q, nil
}

// The first page pins its effective end even if a future end_time was supplied.
// Both the original request and the returned canonical bounds can resume it.
type usageRecordsCursor struct {
	Start        time.Time        `json:"s"`
	End          time.Time        `json:"e"`
	RequestedEnd time.Time        `json:"r"`
	CreatedAt    time.Time        `json:"t"`
	ID           int64            `json:"i"`
	Columns      *usageCSVColumns `json:"cols,omitempty"`
}

func (h *UsageRecordsHandler) cursorMAC(payload []byte, userID, keyID int64) []byte {
	mac := hmac.New(sha256.New, h.cursorSecret)
	_, _ = fmt.Fprintf(mac, "usage-records/v1/%d/%d/", userID, keyID)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func (h *UsageRecordsHandler) nextCursor(q usageRecordsQuery, last usageRecord, userID, keyID int64) (string, error) {
	payload, err := json.Marshal(usageRecordsCursor{Start: q.start, End: q.end, RequestedEnd: q.requestedEnd, CreatedAt: last.CreatedAt, ID: last.ID, Columns: q.columns})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(append(payload, h.cursorMAC(payload, userID, keyID)...)), nil
}

func (h *UsageRecordsHandler) resumeCursor(q *usageRecordsQuery, token string, userID, keyID int64) bool {
	if len(token) > 1024 {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) <= sha256.Size {
		return false
	}
	payload, sig := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	if !hmac.Equal(sig, h.cursorMAC(payload, userID, keyID)) {
		return false
	}
	var cursor usageRecordsCursor
	if json.Unmarshal(payload, &cursor) != nil || !q.start.Equal(cursor.Start) ||
		(!q.requestedEnd.Equal(cursor.RequestedEnd) && !q.requestedEnd.Equal(cursor.End)) ||
		(q.format == "csv") != (cursor.Columns != nil) ||
		cursor.ID <= 0 || cursor.CreatedAt.Before(q.start) || !cursor.CreatedAt.Before(cursor.End) {
		return false
	}
	q.end, q.requestedEnd = cursor.End, cursor.RequestedEnd
	q.columns = cursor.Columns
	q.after = &service.UsageRecordPosition{CreatedAt: cursor.CreatedAt, ID: cursor.ID}
	return true
}
