package circadian

import (
	"math"
	"sort"
	"time"

	"github.com/kungfusheep/hue/client"
)

type Target struct {
	Phase      string     `json:"phase"`
	Brightness float64    `json:"brightness"`
	Mirek      float64    `json:"mirek,omitempty"`
	XY         *client.XY `json:"xy,omitempty"`
	ColorOnly  bool       `json:"color_only"`
}

type anchor struct {
	at    time.Time
	phase string
}

type ScheduledPhase struct {
	Phase string    `json:"phase"`
	At    time.Time `json:"at"`
}

// ScheduleForDay lists the local calendar day's effective lighting boundaries.
func ScheduleForDay(c Config, date time.Time) []ScheduledPhase {
	location, _ := time.LoadLocation(c.Timezone)
	var result []ScheduledPhase
	for _, point := range dailyAnchors(c, date.In(location)) {
		result = append(result, ScheduledPhase{Phase: point.phase, At: point.at})
	}
	return result
}

// wallTime selects the first occurrence of a repeated minute and the first valid
// minute after a missing clock time on a daylight-saving day.
func wallTime(date time.Time, clock string) time.Time {
	parsed, _ := time.Parse("15:04", clock)
	wanted := parsed.Hour()*60 + parsed.Minute()
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	end := start.AddDate(0, 0, 1)
	for t := start; t.Before(end); t = t.Add(time.Minute) {
		if t.Hour()*60+t.Minute() >= wanted {
			return t
		}
	}
	return end
}

// wakeAndBed returns the day's sunrise (with offset), wake time, and the first bedtime after waking.
func wakeAndBed(c Config, date time.Time) (rise, wake, bed time.Time) {
	rise = wallTime(date, c.FixedSunrise)
	if c.Latitude != nil {
		if value, ok := solarTime(date, *c.Latitude, *c.Longitude, true); ok {
			rise = value
		}
	}
	riseOffset, _ := time.ParseDuration(c.SunriseOffset)
	rise = rise.Add(riseOffset)
	if c.WakeTime == "sunrise" {
		wake = rise
	} else {
		wake = wallTime(date, c.WakeTime)
	}
	bed = wallTime(date, c.Bedtime)
	if !bed.After(wake) {
		bed = wallTime(date.AddDate(0, 0, 1), c.Bedtime)
	}
	return rise, wake, bed
}

func dailyAnchors(c Config, date time.Time) []anchor {
	rise, wake, bed := wakeAndBed(c, date)
	set := wallTime(date, c.FixedSunset)
	if !set.After(wake) {
		set = wallTime(date.AddDate(0, 0, 1), c.FixedSunset)
	}
	if c.Latitude != nil {
		setDate := date
		if set.Day() != date.Day() {
			setDate = date.AddDate(0, 0, 1)
		}
		if value, ok := solarTime(setDate, *c.Latitude, *c.Longitude, false); ok {
			set = value
		}
	}
	setOffset, _ := time.ParseDuration(c.SunsetOffset)
	set = set.Add(setOffset)
	interval := time.Hour
	if available := bed.Sub(wake) / 6; available < interval {
		interval = available
	}
	// Keep all six phases ordered inside the explicit waking interval, even in polar seasons.
	if set.Before(wake.Add(4 * interval)) {
		set = wake.Add(4 * interval)
	}
	if set.After(bed.Add(-2 * interval)) {
		set = bed.Add(-2 * interval)
	}
	day := wake.Add(interval)
	if rise.After(day) {
		day = rise
	}
	if day.After(set.Add(-3 * interval)) {
		day = set.Add(-3 * interval)
	}
	return []anchor{{wake, "morning"}, {day, "daytime"}, {set.Add(-2 * interval), "late_afternoon"},
		{set, "sunset"}, {set.Add(interval), "evening"}, {bed, "night"}}
}

// TargetAt calculates the daily curve without reading or writing the Bridge.
func TargetAt(c Config, now time.Time, room RoomConfig) Target {
	location, _ := time.LoadLocation(c.Timezone)
	now = now.In(location)
	var anchors []anchor
	for offset := -2; offset <= 1; offset++ {
		anchors = append(anchors, dailyAnchors(c, now.AddDate(0, 0, offset))...)
	}
	sort.Slice(anchors, func(i, j int) bool { return anchors[i].at.Before(anchors[j].at) })
	index := 0
	for i, a := range anchors {
		if !a.at.After(now) {
			index = i
		}
	}
	current := anchors[index]
	profile := func(phase string) Profile {
		if p, ok := room.Profiles[phase]; ok {
			return p
		}
		return c.Profiles[phase]
	}
	end := target(profile(current.phase), current.phase)
	if index == 0 {
		return end
	}
	duration, _ := time.ParseDuration(c.Transition)
	gap := current.at.Sub(anchors[index-1].at)
	if duration > gap {
		duration = gap
	}
	u := math.Max(0, math.Min(1, float64(now.Sub(current.at))/float64(duration)))
	if u >= 1 {
		return end
	}
	start := target(profile(anchors[index-1].phase), anchors[index-1].phase)
	s := u * u * (3 - 2*u)
	end.Brightness = start.Brightness + s*(end.Brightness-start.Brightness)
	if start.XY == nil && end.XY == nil {
		end.Mirek = start.Mirek + s*(end.Mirek-start.Mirek)
		return end
	}
	a, b := targetXY(start), targetXY(end)
	end.XY = &client.XY{X: a.X + s*(b.X-a.X), Y: a.Y + s*(b.Y-a.Y)}
	// Warm-white devices can finish a white phase but cannot reproduce a colour-only phase.
	end.ColorOnly = current.phase == "evening" || current.phase == "night" || profile(current.phase).Color != ""
	return end
}

func target(p Profile, phase string) Target {
	result := Target{Phase: phase, Brightness: p.Brightness, ColorOnly: p.Color != ""}
	if p.Kelvin > 0 {
		result.Mirek = 1e6 / p.Kelvin
	} else {
		xy, _ := client.ParseColor(p.Color, nil)
		result.XY = &xy
	}
	return result
}

func targetXY(t Target) client.XY {
	if t.XY != nil {
		return *t.XY
	}
	kelvin := math.Max(1667, math.Min(25000, 1e6/t.Mirek))
	// Planckian-locus approximation; this describes appearance, not a measured spectrum.
	var x float64
	if kelvin <= 4000 {
		x = -0.2661239e9/math.Pow(kelvin, 3) - 0.2343580e6/(kelvin*kelvin) + 0.8776956e3/kelvin + 0.179910
	} else {
		x = -3.0258469e9/math.Pow(kelvin, 3) + 2.1070379e6/(kelvin*kelvin) + 0.2226347e3/kelvin + 0.240390
	}
	var y float64
	if kelvin <= 2222 {
		y = -1.1063814*x*x*x - 1.3481102*x*x + 2.18555832*x - 0.20219683
	} else if kelvin <= 4000 {
		y = -0.9549476*x*x*x - 1.37418593*x*x + 2.09137015*x - 0.16748867
	} else {
		y = 3.081758*x*x*x - 5.8733867*x*x + 3.75112997*x - 0.37001483
	}
	return client.XY{X: x, Y: y}
}
