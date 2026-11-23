package service

import (
	"context"
	"errors"
	"time"
)

// UsageRecordPosition is the exclusive lower bound for a subsequent page.
type UsageRecordPosition struct {
	CreatedAt time.Time
	ID        int64
}

// UsageRecordsReader is intentionally separate from the aggregate/panel queries.
type UsageRecordsReader interface {
	ListUsageRecords(ctx context.Context, userID, keyID int64, start, end time.Time, after *UsageRecordPosition, limit int) ([]UsageLog, error)
}

func (s *UsageService) ListUsageRecords(ctx context.Context, userID, keyID int64, start, end time.Time, after *UsageRecordPosition, limit int) ([]UsageLog, error) {
	reader, ok := s.usageRepo.(UsageRecordsReader)
	if !ok {
		return nil, errors.New("usage record reader unavailable")
	}
	return reader.ListUsageRecords(ctx, userID, keyID, start, end, after, limit)
}
