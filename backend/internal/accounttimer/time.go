// Package accounttimer implements persistent account scheduling without model calls or browser timers.
package accounttimer

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Error struct{ Code string }

func (e *Error) Error() string { return "account timer: " + e.Code }
func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "internal"
}
func invalid(code string) error { return &Error{Code: code} }

type Policy struct {
	MaxDelay  time.Duration
	OpenGrace time.Duration
}

func DefaultPolicy() Policy { return Policy{366 * 24 * time.Hour, 15 * time.Minute} }

const PollInterval = 30 * time.Second

type Input struct {
	Mode     string `json:"mode"`
	Hours    int64  `json:"hours"`
	Minutes  int64  `json:"minutes"`
	Date     string `json:"date"`
	Time     string `json:"time"`
	Timezone string `json:"timezone"`
}
type Preview struct {
	ExecuteAt time.Time `json:"execute_at"`
	Timezone  string    `json:"timezone"`
	LocalDate string    `json:"local_date"`
	LocalTime string    `json:"local_time"`
	Offset    string    `json:"offset"`
	ServerNow time.Time `json:"server_now"`
}

// ConfiguredPolicy bounds the operator-configurable planning horizon. Invalid values
// fail closed to the documented default; the maximum remains ten years.
func ConfiguredPolicy() Policy {
	p := DefaultPolicy()
	if days, err := strconv.Atoi(os.Getenv("ACCOUNT_SCHEDULABLE_TIMER_MAX_DAYS")); err == nil && days >= 1 && days <= 3660 {
		p.MaxDelay = time.Duration(days) * 24 * time.Hour
	}
	return p
}
func Resolve(in Input, now time.Time, p Policy) (Preview, error) {
	var out Preview
	if in.Timezone == "" || in.Timezone == "Local" || (in.Timezone != "UTC" && !strings.Contains(in.Timezone, "/")) {
		return out, invalid("timezone")
	}
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return out, invalid("timezone")
	}
	var due time.Time
	switch in.Mode {
	case "relative":
		maxMinutes := int64(p.MaxDelay / time.Minute)
		if in.Date != "" || in.Time != "" || in.Hours < 0 || in.Minutes < 0 || in.Minutes > maxMinutes || in.Hours > maxMinutes/60 {
			return out, invalid("duration")
		}
		minutes := in.Hours*60 + in.Minutes
		if minutes <= 0 || minutes > maxMinutes {
			return out, invalid("duration")
		}
		due = now.Add(time.Duration(minutes) * time.Minute)
	case "absolute":
		if in.Hours != 0 || in.Minutes != 0 {
			return out, invalid("datetime")
		}
		date := in.Date
		if date == "" {
			date = now.In(loc).Format("2006-01-02")
		}
		due, err = resolveWall(date, in.Time, loc)
		if err != nil {
			return out, err
		}
		if in.Date == "" && !due.After(now) {
			day, _ := time.Parse("2006-01-02", date)
			due, err = resolveWall(day.AddDate(0, 0, 1).Format("2006-01-02"), in.Time, loc)
			if err != nil {
				return out, err
			}
		}
	case "daily":
		if in.Date != "" || in.Hours != 0 || in.Minutes != 0 {
			return out, invalid("datetime")
		}
		due, err = nextDaily(in.Time, loc, now)
		if err != nil {
			return out, err
		}
	default:
		return out, invalid("mode")
	}
	if !due.After(now) {
		return out, invalid("past")
	}
	if in.Mode != "daily" && due.Sub(now) > p.MaxDelay {
		return out, invalid("duration")
	}
	local := due.In(loc)
	return Preview{due.UTC(), in.Timezone, local.Format("2006-01-02"), local.Format("15:04"), local.Format("-07:00"), now.UTC()}, nil
}

// nextDaily advances by local calendar dates, never by a fixed 24-hour duration.
// A gap/fold day has no unambiguous execution time, so that date is skipped.
// The bounded search also handles whole-day political timezone changes without
// allowing malformed persisted state to keep a worker running indefinitely.
func nextDaily(clock string, loc *time.Location, after time.Time) (time.Time, error) {
	parsed, err := time.Parse("15:04", clock)
	if err != nil || parsed.Format("15:04") != clock {
		return time.Time{}, invalid("datetime")
	}
	day, err := time.Parse("2006-01-02", after.In(loc).Format("2006-01-02"))
	if err != nil {
		return time.Time{}, invalid("datetime")
	}
	for offset := 0; offset < 8; offset++ {
		due, wallErr := resolveWall(day.AddDate(0, 0, offset).Format("2006-01-02"), clock, loc)
		if ErrorCode(wallErr) == "dst" {
			continue
		}
		if wallErr != nil {
			return time.Time{}, wallErr
		}
		if due.After(after) {
			return due.UTC(), nil
		}
	}
	return time.Time{}, invalid("dst")
}

// Enumerate offsets around the requested wall date, then round-trip every
// candidate. time.Date alone silently normalizes gaps and chooses one fold.
func resolveWall(date, clock string, loc *time.Location) (time.Time, error) {
	text := date + " " + clock
	wall, err := time.Parse("2006-01-02 15:04", text)
	if err != nil || wall.Format("2006-01-02 15:04") != text {
		return time.Time{}, invalid("datetime")
	}
	offsets := map[int]bool{}
	for t := wall.Add(-48 * time.Hour); !t.After(wall.Add(48 * time.Hour)); t = t.Add(30 * time.Minute) {
		_, off := t.In(loc).Zone()
		offsets[off] = true
	}
	matches := []time.Time{}
	for off := range offsets {
		candidate := wall.Add(-time.Duration(off) * time.Second)
		if candidate.In(loc).Format("2006-01-02 15:04") == text {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return time.Time{}, invalid("dst")
	}
	return matches[0], nil
}

// Opening changes only schedulable, and never makes an inactive/expired account
// eligible. Quota, cooldown, ACL and quality gates remain untouched.
func Decision(open bool, due, now time.Time, status string, expires *time.Time, deleted bool, p Policy) string {
	if deleted {
		return "account_deleted"
	}
	if open {
		if now.Sub(due) > p.OpenGrace {
			return "open_expired"
		}
		if status != "active" {
			return "account_inactive"
		}
		if expires != nil && !expires.After(now) {
			return "account_expired"
		}
	}
	return ""
}
