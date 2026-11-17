package timezone

import "time"

// CalendarDayStart returns the first instant of a local calendar date. Midnight
// may be skipped or repeated by a timezone transition, so time.Date alone does
// not always identify the beginning of the requested date.
func CalendarDayStart(year int, month time.Month, day int, loc *time.Location) time.Time {
	target := calendarDateOrdinal(year, month, day)
	ordinal := func(at time.Time) int {
		y, m, d := at.In(loc).Date()
		return calendarDateOrdinal(y, m, d)
	}
	start := time.Date(year, month, day, 0, 0, 0, 0, loc)
	if ordinal(start) == target && ordinal(start.Add(-time.Nanosecond)) < target {
		return start
	}
	low := time.Date(year, month, day-1, 12, 0, 0, 0, loc)
	high := time.Date(year, month, day, 12, 0, 0, 0, loc)
	for high.Sub(low) > time.Nanosecond {
		mid := low.Add(high.Sub(low) / 2)
		if ordinal(mid) >= target {
			high = mid
		} else {
			low = mid
		}
	}
	return high
}

func calendarDateOrdinal(year int, month time.Month, day int) int {
	return year*10000 + int(month)*100 + day
}
