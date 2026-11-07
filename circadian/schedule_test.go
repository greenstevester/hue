package circadian

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTargetUsesSixPhasesAndSmoothstep(t *testing.T) {
	c := DefaultConfig()
	c.SunsetOffset = "0m"
	for _, test := range []struct {
		at, phase  string
		brightness float64
	}{
		{"2026-01-10T06:00:00+01:00", "night", 3},
		{"2026-01-10T07:30:00+01:00", "morning", 90},
		{"2026-01-10T08:30:00+01:00", "daytime", 85},
		{"2026-01-10T16:30:00+01:00", "late_afternoon", 50},
		{"2026-01-10T18:30:00+01:00", "sunset", 35},
		{"2026-01-10T19:30:00+01:00", "evening", 15},
		{"2026-01-10T23:00:00+01:00", "night", 3},
		{"2026-01-10T08:15:00+01:00", "daytime", 87.5},
	} {
		got := TargetAt(c, instant(t, test.at), c.Rooms[0])
		if got.Phase != test.phase || math.Abs(got.Brightness-test.brightness) > 0.001 {
			t.Fatalf("%s: %+v", test.at, got)
		}
	}
}

func TestWallTimeHandlesDaylightSavingGapAndRepeatedMinute(t *testing.T) {
	location, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ date, clock, want string }{
		{"2026-03-29T12:00:00+02:00", "02:30", "2026-03-29T03:00:00+02:00"},
		{"2026-10-25T12:00:00+01:00", "02:30", "2026-10-25T02:30:00+02:00"},
	} {
		got := wallTime(instant(t, test.date).In(location), test.clock)
		if !got.Equal(instant(t, test.want)) {
			t.Fatalf("got %s want %s", got, test.want)
		}
	}
}

func TestSunriseWakeAcceptsTheZurichConfiguration(t *testing.T) {
	if _, err := LoadConfig(filepath.Join("..", "config", "circadian-zurich.json")); err != nil {
		t.Fatal(err)
	}
}

func TestOvernightScheduleAndRoomProfile(t *testing.T) {
	c := DefaultConfig()
	c.WakeTime = "20:00"
	c.Bedtime = "08:00"
	room := c.Rooms[0]
	room.Profiles = map[string]Profile{"morning": {Kelvin: 4000, Brightness: 70}}
	got := TargetAt(c, instant(t, "2026-10-25T20:40:00+01:00"), room)
	if got.Phase != "morning" || got.Brightness != 70 {
		t.Fatalf("%+v", got)
	}
	got = TargetAt(c, instant(t, "2026-10-26T09:00:00+01:00"), room)
	if got.Phase != "night" {
		t.Fatalf("%+v", got)
	}
}

func TestSolarTimeMatchesReferenceAndHandlesPolarNight(t *testing.T) {
	// NOAA fractional-year worked example: equinox, Greenwich, sunrise about 06:00 UTC.
	date := instant(t, "2026-03-20T12:00:00Z")
	rise, ok := solarTime(date, 0, 0, true)
	if !ok || math.Abs(rise.Sub(instant(t, "2026-03-20T06:04:00Z")).Minutes()) > 5 {
		t.Fatalf("sunrise %s", rise)
	}
	set, ok := solarTime(date, 0, 0, false)
	if !ok || math.Abs(set.Sub(instant(t, "2026-03-20T18:11:00Z")).Minutes()) > 5 {
		t.Fatalf("sunset %s", set)
	}
	if _, ok := solarTime(instant(t, "2026-12-21T12:00:00Z"), 89, 0, true); ok {
		t.Fatal("polar night has no sunrise")
	}
	c := DefaultConfig()
	lat, lon := 89.0, 0.0
	c.Latitude = &lat
	c.Longitude = &lon
	got := TargetAt(c, instant(t, "2026-12-21T12:00:00Z"), c.Rooms[0])
	if got.Phase != "daytime" {
		t.Fatalf("fallback failed: %+v", got)
	}
}

func TestScheduleAnchorsStayOrderedAcrossSolarSeasons(t *testing.T) {
	c := DefaultConfig()
	lat, lon := 47.3769, 8.5417
	c.Latitude = &lat
	c.Longitude = &lon
	loc, _ := time.LoadLocation(c.Timezone)
	for day := 0; day < 366; day++ {
		date := time.Date(2026, 1, 1, 12, 0, 0, 0, loc).AddDate(0, 0, day)
		anchors := dailyAnchors(c, date)
		for i := 1; i < len(anchors); i++ {
			if !anchors[i].at.After(anchors[i-1].at) {
				t.Fatalf("unordered on %s: %+v", date, anchors)
			}
		}
	}
}

func TestSunriseWakeTracksLocalSunriseAndOffset(t *testing.T) {
	c := DefaultConfig()
	c.WakeTime = "sunrise"
	latitude, longitude := 47.3769, 8.5417
	c.Latitude = &latitude
	c.Longitude = &longitude
	location, _ := time.LoadLocation(c.Timezone)
	for _, value := range []string{
		"2026-03-28T12:00:00+01:00", "2026-03-29T12:00:00+02:00",
		"2026-06-21T12:00:00+02:00", "2026-11-07T12:00:00+01:00", "2026-12-21T12:00:00+01:00",
	} {
		date := instant(t, value).In(location)
		sunrise, ok := solarTime(date, latitude, longitude, true)
		if !ok {
			t.Fatal("missing Zurich sunrise")
		}
		anchors := dailyAnchors(c, date)
		if !anchors[0].at.Equal(sunrise) {
			t.Fatalf("wake %s differs from sunrise %s", anchors[0].at, sunrise)
		}
		before := TargetAt(c, sunrise.Add(-time.Second), c.Rooms[0])
		after := TargetAt(c, sunrise.Add(time.Second), c.Rooms[0])
		if before.Phase != "night" || after.Phase != "morning" {
			t.Fatalf("sunrise boundary: %s -> %s", before.Phase, after.Phase)
		}
		c.SunriseOffset = "-15m"
		shifted := dailyAnchors(c, date)
		if !shifted[0].at.Equal(sunrise.Add(-15 * time.Minute)) {
			t.Fatal("sunrise offset not applied to wake")
		}
		c.SunriseOffset = "0m"
	}
}

func TestZurichNovemberSunriseMatchesPublishedTimes(t *testing.T) {
	c := DefaultConfig()
	c.WakeTime = "sunrise"
	latitude, longitude := 47.3769, 8.5417
	c.Latitude = &latitude
	c.Longitude = &longitude
	// Time and Date's Zurich November 2026 table, checked 2026-11-07.
	for _, test := range []struct{ date, want string }{
		{"2026-11-07T12:00:00+01:00", "2026-11-07T07:18:00+01:00"},
		{"2026-11-08T12:00:00+01:00", "2026-11-08T07:19:00+01:00"},
	} {
		schedule := ScheduleForDay(c, instant(t, test.date))
		wake := schedule[0]
		if wake.Phase != "morning" || math.Abs(wake.At.Sub(instant(t, test.want)).Minutes()) > 3 {
			t.Fatalf("Zurich sunrise %s differs from reference %s", wake.At, test.want)
		}
		t.Logf("%s: calculated sunrise/wake %s", test.date[:10], wake.At.Format("15:04:05 MST"))
	}
}
