package client

import (
	"encoding/json"
	"math"
	"testing"
)

func TestParseColorCorrectsFractionalGammaAndValidatesInput(t *testing.T) {
	got, err := ParseColor("#804020", nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.X-.505391) > 0.000001 || math.Abs(got.Y-.384267) > 0.000001 {
		t.Fatalf("mixed colour: %+v", got)
	}
	for _, bad := range []string{"", "#GG0000", "12345", "1234567"} {
		if _, err := ParseColor(bad, nil); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestClipXYProjectsToGamutEdge(t *testing.T) {
	gamut := &Gamut{Red: XY{X: .6, Y: .3}, Green: XY{X: .3, Y: .6}, Blue: XY{X: .15, Y: .1}}
	inside := XY{X: .3, Y: .3}
	if ClipXY(inside, gamut) != inside {
		t.Fatal("inside colour changed")
	}
	got := ClipXY(XY{X: .9, Y: .3}, gamut)
	if math.Abs(got.X-.6) > 1e-9 || math.Abs(got.Y-.3) > 1e-9 {
		t.Fatalf("%+v", got)
	}
}

func TestLightDecodingTracksFeaturesAndWritesOnlyWritableFields(t *testing.T) {
	var light Light
	resource := `{"id":"strip","dimming":{"brightness":3,"min_dim_level":2},
		"gradient":{"points_capable":3},"color_temperature":{"mirek":null,"mirek_valid":false,
		"mirek_schema":{"mirek_minimum":153,"mirek_maximum":454}}}`
	if err := json.Unmarshal([]byte(resource), &light); err != nil {
		t.Fatal(err)
	}
	if !light.HasDimming || len(light.Gradient) == 0 || light.ColorTemperature.MirekSchema == nil {
		t.Fatalf("%+v", light)
	}
	raw, err := json.Marshal(LightUpdate{
		ColorTemperature: &ColorTemperature{Mirek: 400, MirekSchema: &MirekSchema{MirekMinimum: 153}},
		Dimming:          &Dimming{Brightness: 3, MinDimLevel: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields["color_temperature"]) != 1 || len(fields["dimming"]) != 1 {
		t.Fatalf("read-only fields sent: %s", raw)
	}
	if err := json.Unmarshal([]byte(`{"id":"white"}`), &light); err != nil {
		t.Fatal(err)
	}
	if light.HasDimming || light.ColorTemperature != nil {
		t.Fatal("absent features retained")
	}
}

func TestClipXYProjectsOntoEdgeBetweenCorners(t *testing.T) {
	gamut := &Gamut{Red: XY{X: .6, Y: .3}, Green: XY{X: .3, Y: .6}, Blue: XY{X: .15, Y: .1}}
	// The nearest in-range colour lies halfway along the red-green edge, not at a corner.
	got := ClipXY(XY{X: .5, Y: .5}, gamut)
	if math.Abs(got.X-.45) > 1e-9 || math.Abs(got.Y-.45) > 1e-9 {
		t.Fatalf("got %+v, want (0.45, 0.45)", got)
	}
}
