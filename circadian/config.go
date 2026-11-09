package circadian

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/kungfusheep/hue/client"
)

type Profile struct {
	Kelvin     float64 `json:"kelvin,omitempty"`
	Color      string  `json:"color,omitempty"`
	Brightness float64 `json:"brightness"`
}

type RoomConfig struct {
	Name     string             `json:"name"`
	ID       string             `json:"id,omitempty"`
	Lights   []string           `json:"lights,omitempty"` // when set, only these lights in the room
	Profiles map[string]Profile `json:"profiles,omitempty"`
}

type Config struct {
	Enabled          bool               `json:"enabled"`
	Timezone         string             `json:"timezone"`
	Latitude         *float64           `json:"latitude"`
	Longitude        *float64           `json:"longitude"`
	WakeTime         string             `json:"wake_time"`
	Bedtime          string             `json:"bedtime"`
	FixedSunrise     string             `json:"fixed_sunrise"`
	FixedSunset      string             `json:"fixed_sunset"`
	SunriseOffset    string             `json:"sunrise_offset"`
	SunsetOffset     string             `json:"sunset_offset"`
	Transition       string             `json:"transition"`
	OverrideDuration string             `json:"override_duration"`
	Rooms            []RoomConfig       `json:"rooms"`
	ExcludeLights    []string           `json:"exclude_lights"`
	Profiles         map[string]Profile `json:"profiles"`
}

var phases = []string{"morning", "daytime", "late_afternoon", "sunset", "evening", "night"}

func DefaultConfig() Config {
	return Config{Timezone: "Europe/Zurich", WakeTime: "07:00", Bedtime: "22:30",
		FixedSunrise: "08:00", FixedSunset: "18:00", SunriseOffset: "0m", SunsetOffset: "-60m",
		Transition: "30m", OverrideDuration: "2h",
		Rooms: []RoomConfig{{Name: "Kitchen"}, {Name: "Bedroom"}},
		Profiles: map[string]Profile{
			"morning": {Kelvin: 4000, Brightness: 90}, "daytime": {Kelvin: 3500, Brightness: 85},
			"late_afternoon": {Kelvin: 2700, Brightness: 50}, "sunset": {Kelvin: 2200, Brightness: 35},
			"evening": {Color: "#FF7800", Brightness: 15}, "night": {Color: "#FF1600", Brightness: 3},
		},
	}
}

func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return c, err
	}
	var extra interface{}
	if err = decoder.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("configuration must contain one JSON object")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	location, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return err
	}
	for _, value := range []string{c.Bedtime, c.FixedSunrise, c.FixedSunset} {
		if _, err := time.Parse("15:04", value); err != nil {
			return fmt.Errorf("invalid clock time %q", value)
		}
	}
	if c.WakeTime == "sunrise" {
		if c.Latitude == nil || c.Longitude == nil {
			return fmt.Errorf("sunrise wake requires latitude and longitude")
		}
	} else {
		wake, err := time.Parse("15:04", c.WakeTime)
		if err != nil {
			return fmt.Errorf("invalid wake time %q", c.WakeTime)
		}
		bed, _ := time.Parse("15:04", c.Bedtime)
		awake := bed.Sub(wake)
		if awake < 0 {
			awake += 24 * time.Hour
		}
		if awake < 6*time.Hour {
			return fmt.Errorf("waking interval must be at least six hours")
		}
	}
	for _, value := range []string{c.SunriseOffset, c.SunsetOffset} {
		d, err := time.ParseDuration(value)
		if err != nil || d < -12*time.Hour || d > 12*time.Hour {
			return fmt.Errorf("invalid solar offset %q", value)
		}
	}
	for _, value := range []string{c.Transition, c.OverrideDuration} {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 || d > 24*time.Hour {
			return fmt.Errorf("invalid duration %q", value)
		}
	}
	if (c.Latitude == nil) != (c.Longitude == nil) {
		return fmt.Errorf("latitude and longitude must be supplied together")
	}
	if c.Latitude != nil && (!finite(*c.Latitude) || !finite(*c.Longitude) ||
		math.Abs(*c.Latitude) > 90 || math.Abs(*c.Longitude) > 180) {
		return fmt.Errorf("coordinates outside supported range")
	}
	if c.WakeTime == "sunrise" {
		if err := c.checkSunriseDays(location); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, r := range c.Rooms {
		name := strings.ToLower(r.Name)
		if !controlledRoom(name) || seen[name] {
			return fmt.Errorf("only Kitchen, Bedroom and Lounge may be controlled, each once")
		}
		seen[name] = true
		for phase, p := range r.Profiles {
			if err := validateProfile(phase, p); err != nil {
				return err
			}
		}
	}
	if !seen["kitchen"] || !seen["bedroom"] {
		return fmt.Errorf("configure Kitchen and Bedroom")
	}
	if len(c.Profiles) != len(phases) {
		return fmt.Errorf("all six lighting profiles are required")
	}
	for phase, p := range c.Profiles {
		if err := validateProfile(phase, p); err != nil {
			return err
		}
	}
	return nil
}

// checkSunriseDays applies the six-hour rule to every day of a reference leap year, because a
// sunrise wake moves with the seasons. Sunrise on a given date shifts by a minute or two between years.
func (c Config) checkSunriseDays(location *time.Location) error {
	for day := time.Date(2028, 1, 1, 12, 0, 0, 0, location); day.Year() == 2028; day = day.AddDate(0, 0, 1) {
		_, wake, bed := wakeAndBed(c, day)
		if bed.Sub(wake) < 6*time.Hour {
			return fmt.Errorf("wake at sunrise leaves less than six hours before bedtime around %s",
				day.Format("2 January"))
		}
	}
	return nil
}

// controlledRoom names the rooms the schedule may control.
// Kitchen and Bedroom are required; Lounge is optional.
func controlledRoom(name string) bool {
	switch strings.ToLower(name) {
	case "kitchen", "bedroom", "lounge":
		return true
	}
	return false
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func validateProfile(phase string, p Profile) error {
	known := false
	for _, name := range phases {
		known = known || name == phase
	}
	if !known || !finite(p.Brightness) || p.Brightness < 0 || p.Brightness > 100 {
		return fmt.Errorf("invalid profile %q", phase)
	}
	if (p.Kelvin > 0) == (p.Color != "") || !finite(p.Kelvin) {
		return fmt.Errorf("profile %s needs exactly one temperature or colour", phase)
	}
	if p.Kelvin > 0 && (p.Kelvin < 1000 || p.Kelvin > 10000) {
		return fmt.Errorf("temperature outside 1000–10000 K")
	}
	if p.Color != "" {
		if strings.TrimPrefix(p.Color, "#") == "000000" && p.Brightness > 0 {
			return fmt.Errorf("black requires zero brightness")
		}
		_, err := client.ParseColor(p.Color, nil)
		return err
	}
	return nil
}
