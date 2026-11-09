package test

import (
	"testing"
	"time"

	"cpa-usage-keeper/internal/timeutil"
)

func TestDayBoundsUsesRealCalendarDate(t *testing.T) {
	for _, sample := range []struct {
		zone, date, start, end string
	}{
		{"Asia/Shanghai", "2026-03-08", "2026-03-08T00:00:00+08:00", "2026-03-09T00:00:00+08:00"},
		{"America/Los_Angeles", "2026-03-08", "2026-03-08T00:00:00-08:00", "2026-03-09T00:00:00-07:00"},
		{"America/Los_Angeles", "2026-11-01", "2026-11-01T00:00:00-07:00", "2026-11-02T00:00:00-08:00"},
		{"Europe/Berlin", "2026-10-25", "2026-10-25T00:00:00+02:00", "2026-10-26T00:00:00+01:00"},
		{"America/Santiago", "2026-09-06", "2026-09-06T01:00:00-03:00", "2026-09-07T00:00:00-03:00"},
		{"Australia/Lord_Howe", "2026-10-04", "2026-10-04T00:00:00+10:30", "2026-10-05T00:00:00+11:00"},
		{"Australia/Lord_Howe", "2026-04-05", "2026-04-05T00:00:00+11:00", "2026-04-06T00:00:00+10:30"},
		{"Asia/Kathmandu", "2026-03-08", "2026-03-08T00:00:00+05:45", "2026-03-09T00:00:00+05:45"},
		// Use an observed historical fallback: tzdb 2026f canceled Cuba's
		// November 2026 fallback, so that date no longer exercises repeated midnight.
		{"America/Havana", "2020-11-01", "2020-11-01T00:00:00-04:00", "2020-11-02T00:00:00-05:00"},
	} {
		t.Run(sample.zone+"/"+sample.date, func(t *testing.T) {
			location, err := time.LoadLocation(sample.zone)
			if err != nil {
				t.Fatal(err)
			}
			date, err := timeutil.ParseCivilDate(sample.date)
			if err != nil {
				t.Fatal(err)
			}
			bounds, ok := timeutil.DayBounds(date, location)
			if !ok || bounds.Start.Format(time.RFC3339) != sample.start || bounds.End.Format(time.RFC3339) != sample.end {
				t.Fatalf("bounds=%+v present=%v, want %s..%s", bounds, ok, sample.start, sample.end)
			}
			for at := bounds.Start; at.Before(bounds.End); at = at.Add(15 * time.Minute) {
				if timeutil.DateOf(at.In(location)) != date {
					t.Fatalf("%s belongs to another date", at)
				}
			}
			if timeutil.DateOf(bounds.Start.Add(-time.Nanosecond).In(location)) == date || timeutil.DateOf(bounds.End.In(location)) == date {
				t.Fatal("day boundary includes an instant outside the civil date")
			}
		})
	}
}

func TestCalendarIterationDoesNotDriftOrInventSkippedDate(t *testing.T) {
	date, _ := timeutil.ParseCivilDate("2026-09-05")
	location, _ := time.LoadLocation("America/Santiago")
	var previous time.Time
	for i := 0; i < 5; i++ {
		bounds, ok := timeutil.DayBounds(date.AddDays(i), location)
		if !ok || (!previous.IsZero() && !previous.Equal(bounds.Start)) {
			t.Fatal("noncontiguous dates", bounds)
		}
		previous = bounds.End
	}
	date, _ = timeutil.ParseCivilDate("2011-12-30")
	location, _ = time.LoadLocation("Pacific/Apia")
	if _, ok := timeutil.DayBounds(date, location); ok {
		t.Fatal("skipped date must not be fabricated")
	}
	before, _ := timeutil.DayBounds(date.AddDays(-1), location)
	after, _ := timeutil.DayBounds(date.AddDays(1), location)
	if !before.End.Equal(after.Start) || date.AddDays(-1).DaysUntil(date.AddDays(1)) != 2 {
		t.Fatal("calendar and elapsed date semantics mixed")
	}
}

// Keep repeated-midnight behavior independent of political changes and host tzdb.
func TestDayBoundsRepeatedMidnightKeepsBothOccurrences(t *testing.T) {
	location := posixLocation(t, "CST5CDT,M3.2.0/0,M11.1.0/1")
	date := timeutil.CivilDate{Year: 2026, Month: time.November, Day: 1}
	bounds, ok := timeutil.DayBounds(date, location)
	if !ok || bounds.Start.Format(time.RFC3339) != "2026-11-01T00:00:00-04:00" || bounds.End.Format(time.RFC3339) != "2026-11-02T00:00:00-05:00" || bounds.End.Sub(bounds.Start) != 25*time.Hour {
		t.Fatalf("repeated-midnight day lost an hour: %+v present=%v", bounds, ok)
	}
	for _, value := range []string{"2026-11-01T00:30:00-04:00", "2026-11-01T00:30:00-05:00"} {
		at, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		if at.Before(bounds.Start) || !at.Before(bounds.End) || timeutil.DateOf(at.In(location)) != date {
			t.Fatalf("midnight occurrence %s missing from bounds %+v", value, bounds)
		}
	}
	before, beforeOK := timeutil.DayBounds(date.AddDays(-1), location)
	after, afterOK := timeutil.DayBounds(date.AddDays(1), location)
	if !beforeOK || !afterOK || !before.End.Equal(bounds.Start) || !bounds.End.Equal(after.Start) {
		t.Fatal("repeated midnight broke adjacent calendar boundaries")
	}
}
