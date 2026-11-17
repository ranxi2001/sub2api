package handler

import (
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// parseAPIKeyUsageRange is shared by list sorting and the displayed usage totals.
// Omitted dates leave the existing rolling 30-day default to the repository.
func parseAPIKeyUsageRange(startDate, endDate, userTimezone string) (time.Time, time.Time, error) {
	if startDate == "" && endDate == "" {
		return time.Time{}, time.Time{}, nil
	}
	if startDate == "" || endDate == "" {
		return time.Time{}, time.Time{}, errors.New("start_date and end_date must be provided together")
	}
	start, err := timezone.ParseInUserLocation("2006-01-02", startDate, userTimezone)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("invalid start_date, use YYYY-MM-DD")
	}
	end, err := timezone.ParseInUserLocation("2006-01-02", endDate, userTimezone)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("invalid end_date, use YYYY-MM-DD")
	}
	if start.After(end) {
		return time.Time{}, time.Time{}, errors.New("start_date must be on or before end_date")
	}
	// Calendar arithmetic includes the end day across daylight-saving transitions.
	return start, end.AddDate(0, 0, 1), nil
}
