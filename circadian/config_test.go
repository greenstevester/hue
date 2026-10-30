package circadian

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRejectsUnapprovedRoomsAndInvalidValues(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Config)
	}{
		{"other room", func(c *Config) { c.Rooms[1].Name = "Office" }},
		{"missing room", func(c *Config) { c.Rooms = c.Rooms[:1] }},
		{"duplicate room", func(c *Config) { c.Rooms[1].Name = "Kitchen" }},
		{"timezone", func(c *Config) { c.Timezone = "invalid/place" }},
		{"missing coordinate", func(c *Config) { v := 47.0; c.Latitude = &v }},
		{"invalid location", func(c *Config) { a, b := 91.0, 0.0; c.Latitude = &a; c.Longitude = &b }},
		{"clock", func(c *Config) { c.WakeTime = "25:00" }},
		{"short waking interval", func(c *Config) { c.Bedtime = "08:00" }},
		{"duration", func(c *Config) { c.Transition = "0m" }},
		{"offset", func(c *Config) { c.SunsetOffset = "30h" }},
		{"colour", func(c *Config) { c.Profiles["night"] = Profile{Color: "no", Brightness: 3} }},
		{"brightness", func(c *Config) { c.Profiles["night"] = Profile{Color: "#FF0000", Brightness: 101} }},
		{"ambiguous profile", func(c *Config) { c.Profiles["night"] = Profile{Color: "#FF0000", Kelvin: 2000} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := DefaultConfig()
			test.change(&c)
			if c.Validate() == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestLoadConfigUsesDefaultsAndRejectsTrailingOrUnknownFields(t *testing.T) {
	for _, test := range []struct {
		input string
		valid bool
	}{
		{`{"enabled":true}`, true}, {`{"enabled":true,"typo":1}`, false},
		{`{} {}`, false}, {`{"enabled":`, false},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(test.input), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := LoadConfig(path)
		if (err == nil) != test.valid {
			t.Fatalf("%s: %v", test.input, err)
		}
		if test.valid && (c.Rooms[0].Name != "Kitchen" || c.Bedtime != "22:30") {
			t.Fatal("defaults lost")
		}
	}
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
}
