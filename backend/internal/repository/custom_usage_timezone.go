package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// configuredTimezone allows a repository to use an immutable location without
// changing process-wide time.Local or the application's startup timezone.
// A nil location preserves the configured server timezone.
type configuredTimezone struct {
	location *time.Location
}

func (z configuredTimezone) timezoneLocation() *time.Location {
	if z.location != nil {
		return z.location
	}
	return timezone.Location()
}

func (z configuredTimezone) timezoneName() string {
	if z.location != nil {
		return z.location.String()
	}
	return timezone.Name()
}

func (z configuredTimezone) startOfDay(at time.Time) time.Time {
	loc := z.timezoneLocation()
	at = at.In(loc)
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, loc)
}

func (z configuredTimezone) groupUsageTodayStart(at time.Time) time.Time {
	year, month, day := at.In(z.timezoneLocation()).Date()
	return timezone.CalendarDayStart(year, month, day, z.timezoneLocation()).UTC()
}

func (z configuredTimezone) groupUsageYesterdayStart(at time.Time) time.Time {
	return z.groupUsageTodayStart(z.groupUsageTodayStart(at).Add(-12 * time.Hour))
}

func (z configuredTimezone) groupUsageDate(at time.Time) string {
	return at.In(z.timezoneLocation()).Format("2006-01-02")
}

func (z configuredTimezone) parseGroupUsageDate(value string) (time.Time, error) {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, err
	}
	year, month, day := date.Date()
	return timezone.CalendarDayStart(year, month, day, z.timezoneLocation()), nil
}
