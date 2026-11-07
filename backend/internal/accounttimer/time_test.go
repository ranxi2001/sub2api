package accounttimer

import (
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	now := time.Date(2026, 10, 21, 23, 30, 0, 0, time.UTC)
	cases := []struct {
		name  string
		input Input
		want  string
		code  string
	}{
		{"8h from server receipt", Input{Mode: "relative", Hours: 8, Timezone: "Asia/Shanghai"}, "2026-10-22T07:30:00Z", ""},
		{"minutes cross midnight", Input{Mode: "relative", Minutes: 45, Timezone: "Asia/Shanghai"}, "2026-10-22T00:15:00Z", ""},
		{"Beijing next 8", Input{Mode: "absolute", Time: "08:00", Timezone: "Asia/Shanghai"}, "2026-10-22T00:00:00Z", ""},
		{"explicit past stays past", Input{Mode: "absolute", Date: "2026-10-21", Time: "08:00", Timezone: "Asia/Shanghai"}, "", "past"},
		{"explicit Beijing date", Input{Mode: "absolute", Date: "2026-10-23", Time: "08:00", Timezone: "Asia/Shanghai"}, "2026-10-23T00:00:00Z", ""},
		{"invalid zone", Input{Mode: "relative", Hours: 8, Timezone: "Mars/City"}, "", "timezone"},
		{"no implicit zone", Input{Mode: "relative", Hours: 8}, "", "timezone"},
		{"DST missing", Input{Mode: "absolute", Date: "2027-03-14", Time: "02:30", Timezone: "America/New_York"}, "", "dst"},
		{"DST duplicate", Input{Mode: "absolute", Date: "2026-11-01", Time: "01:30", Timezone: "America/New_York"}, "", "dst"},
		{"zero", Input{Mode: "relative", Timezone: "UTC"}, "", "duration"},
		{"extreme", Input{Mode: "relative", Hours: 1 << 60, Timezone: "UTC"}, "", "duration"},
		{"negative", Input{Mode: "relative", Hours: -1, Minutes: 90, Timezone: "UTC"}, "", "duration"},
		{"invalid calendar date", Input{Mode: "absolute", Date: "2026-02-30", Time: "08:00", Timezone: "UTC"}, "", "datetime"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Resolve(c.input, now, DefaultPolicy())
			if c.code != "" {
				if ErrorCode(err) != c.code {
					t.Fatalf("want %s, got %v", c.code, err)
				}
				return
			}
			if err != nil || got.ExecuteAt.Format(time.RFC3339) != c.want {
				t.Fatalf("got %+v %v want %s", got, err, c.want)
			}
			if got.Timezone != c.input.Timezone || got.LocalDate == "" || got.Offset == "" {
				t.Fatal("missing explicit zone preview")
			}
		})
	}
}
func TestNextEightCrossesLocalDay(t *testing.T) {
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	p, err := Resolve(Input{Mode: "absolute", Time: "08:00", Timezone: "Asia/Shanghai"}, now, DefaultPolicy())
	if err != nil || p.ExecuteAt.Format(time.RFC3339) != "2026-10-23T00:00:00Z" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestDailyResolvesNextLocalOccurrenceAndSkipsAmbiguousDates(t *testing.T) {
	cases := []struct {
		name, now, clock, zone, want string
	}{
		{"today before opening", "2026-11-05T23:59:00Z", "08:00", "Asia/Shanghai", "2026-11-06T00:00:00Z"},
		{"exact opening advances", "2026-11-06T00:00:00Z", "08:00", "Asia/Shanghai", "2026-11-07T00:00:00Z"},
		{"cross midnight closing", "2026-11-06T00:00:00Z", "01:00", "Asia/Shanghai", "2026-11-06T17:00:00Z"},
		{"UTC midnight", "2026-11-06T23:59:00Z", "00:00", "UTC", "2026-11-07T00:00:00Z"},
		{"spring gap date skipped", "2027-03-14T05:00:00Z", "02:30", "America/New_York", "2027-03-15T06:30:00Z"},
		{"fall duplicate date skipped", "2026-11-01T04:00:00Z", "01:30", "America/New_York", "2026-11-02T06:30:00Z"},
		{"spring local hour after offset change", "2027-03-13T13:01:00Z", "08:00", "America/New_York", "2027-03-14T12:00:00Z"},
		{"fall local hour after offset change", "2026-10-31T12:01:00Z", "08:00", "America/New_York", "2026-11-01T13:00:00Z"},
		{"political whole day skip", "2011-12-30T09:59:00Z", "08:00", "Pacific/Apia", "2011-12-30T18:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, c.now)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := Resolve(Input{Mode: "daily", Time: c.clock, Timezone: c.zone}, now, DefaultPolicy())
			if err != nil || preview.ExecuteAt.Format(time.RFC3339) != c.want || preview.LocalTime != c.clock {
				t.Fatalf("got %+v %v; want %s at %s", preview, err, c.want, c.clock)
			}
		})
	}
}

func TestDailyRejectsMixedModesAndMalformedClocks(t *testing.T) {
	cases := []Input{
		{Mode: "daily", Time: "08:00", Date: "2026-11-07", Timezone: "Asia/Shanghai"},
		{Mode: "daily", Time: "08:00", Hours: 1, Timezone: "Asia/Shanghai"},
		{Mode: "daily", Time: "08:00", Minutes: -1, Timezone: "Asia/Shanghai"},
	}
	for _, clock := range []string{"", "8:00", "08:0", "24:00", "08:00:00", " 08:00"} {
		cases = append(cases, Input{Mode: "daily", Time: clock, Timezone: "UTC"})
	}
	for _, in := range cases {
		if _, err := Resolve(in, time.Now(), DefaultPolicy()); ErrorCode(err) != "datetime" {
			t.Fatalf("accepted invalid daily input %+v: %v", in, err)
		}
	}
}

func TestDailyUsesCalendarRecurrenceRatherThanOneShotPlanningHorizon(t *testing.T) {
	now := time.Date(2027, 3, 14, 5, 0, 0, 0, time.UTC)
	policy := DefaultPolicy()
	policy.MaxDelay = 24 * time.Hour
	preview, err := Resolve(Input{Mode: "daily", Time: "02:30", Timezone: "America/New_York"}, now, policy)
	if err != nil || preview.ExecuteAt.Format(time.RFC3339) != "2027-03-15T06:30:00Z" {
		t.Fatalf("one-shot horizon blocked a valid daily plan: %+v %v", preview, err)
	}
}
func TestExecutionPolicy(t *testing.T) {
	now := time.Now()
	p := DefaultPolicy()
	if Decision(true, now.Add(-16*time.Minute), now, "active", nil, false, p) != "open_expired" {
		t.Fatal("late open must expire")
	}
	if Decision(false, now.Add(-365*24*time.Hour), now, "active", nil, false, p) != "" {
		t.Fatal("late close must run")
	}
	if Decision(true, now, now, "disabled", nil, false, p) != "account_inactive" {
		t.Fatal("must preserve disabled")
	}
	expired := now.Add(-time.Hour)
	if Decision(true, now, now, "active", &expired, false, p) != "account_expired" {
		t.Fatal("must preserve expiry")
	}
	if Decision(false, now, now, "active", nil, true, p) != "account_deleted" {
		t.Fatal("must preserve deletion")
	}
}
