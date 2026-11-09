package circadian

import (
	"testing"
	"time"

	"github.com/kungfusheep/hue/client"
)

func TestAdaptUsesFeaturesAndRespectsPhysicalLimits(t *testing.T) {
	light := client.Light{On: client.OnState{On: true}, HasDimming: true, Dimming: client.Dimming{MinDimLevel: 5},
		ColorTemperature: &client.ColorTemperature{MirekSchema: &client.MirekSchema{MirekMinimum: 153, MirekMaximum: 454}}}
	white := Adapt(Target{Mirek: 555, Brightness: 3}, light, false)
	if white.ColorTemperature == nil || white.ColorTemperature.Mirek != 454 || white.Dimming.Brightness != 5 {
		t.Fatalf("%+v", white)
	}
	xy := client.XY{X: .7, Y: .3}
	red := Adapt(Target{XY: &xy, ColorOnly: true, Brightness: 3}, light, false)
	if red.On == nil || red.On.On || red.Color != nil {
		t.Fatalf("white ambiance must switch off: %+v", red)
	}
	light.Color = &client.Color{Gamut: &client.Gamut{
		Red: client.XY{X: .6, Y: .3}, Green: client.XY{X: .3, Y: .6}, Blue: client.XY{X: .15, Y: .1},
	}}
	color := Adapt(Target{XY: &xy, ColorOnly: true, Brightness: 3}, light, false)
	if color.Color == nil || color.Color.XY.X > .60001 {
		t.Fatalf("%+v", color)
	}
	light.On.On = false
	if got := Adapt(Target{Mirek: 250, Brightness: 90}, light, false); got.On != nil || got.Dimming != nil {
		t.Fatal("manually off light was changed")
	}
	if got := Adapt(Target{Mirek: 250, Brightness: 90}, light, true); got.On == nil || !got.On.On {
		t.Fatal("capability-off light not restored")
	}
}

func TestSelectLightsNeverIncludesOtherRooms(t *testing.T) {
	c := DefaultConfig()
	rooms := []client.Room{
		{ID: "k", Metadata: client.Metadata{Name: "Kitchen"},
			Children: []client.ResourceIdentifier{{RID: "dk", RType: "device"}}},
		{ID: "b", Metadata: client.Metadata{Name: "Bedroom"},
			Children: []client.ResourceIdentifier{{RID: "db", RType: "device"}}},
		{ID: "o", Metadata: client.Metadata{Name: "Office"},
			Children: []client.ResourceIdentifier{{RID: "do", RType: "device"}}},
	}
	devices := []client.Device{}
	lights := []client.Light{}
	for _, suffix := range []string{"k", "b", "o"} {
		devices = append(devices, client.Device{ID: "d" + suffix,
			Services: []client.ResourceIdentifier{{RID: suffix, RType: "light"}}})
		lights = append(lights, client.Light{ID: suffix})
	}
	got, err := SelectLights(c, rooms, devices, lights)
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	for _, l := range got {
		if l.Light.ID == "o" {
			t.Fatal("office selected")
		}
	}
	rooms = append(rooms, rooms[0])
	if _, err := SelectLights(c, rooms, devices, lights); err == nil {
		t.Fatal("ambiguous kitchen accepted")
	}
}

// loungeHouse has one light in the kitchen and bedroom, and a floor lamp ("f") and plug ("p") in the lounge.
func loungeHouse() ([]client.Room, []client.Device, []client.Light) {
	var rooms []client.Room
	var devices []client.Device
	var lights []client.Light
	for room, ids := range map[string][]string{"Kitchen": {"k"}, "Bedroom": {"b"}, "lounge": {"f", "p"}} {
		r := client.Room{ID: room, Metadata: client.Metadata{Name: room}}
		for _, id := range ids {
			r.Children = append(r.Children, client.ResourceIdentifier{RID: "d" + id, RType: "device"})
			devices = append(devices, client.Device{ID: "d" + id,
				Services: []client.ResourceIdentifier{{RID: id, RType: "light"}}})
			lights = append(lights, client.Light{ID: id})
		}
		rooms = append(rooms, r)
	}
	return rooms, devices, lights
}

func TestLoungeListManagesOnlyTheListedLights(t *testing.T) {
	c := DefaultConfig()
	c.Rooms = append(c.Rooms, RoomConfig{Name: "Lounge", Lights: []string{"f"}})
	rooms, devices, lights := loungeHouse()
	got, err := RoomLights(c, rooms, devices, lights)
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]string{}
	for _, l := range got {
		selected[l.Light.ID] = l.Room.Name
	}
	if len(got) != 3 || selected["f"] != "Lounge" || selected["p"] != "" {
		t.Fatalf("selected %v, want the kitchen, the bedroom and only the lounge floor lamp", selected)
	}
	if _, err := NewEngine(c, &fakeBridge{}, got, time.Now); err != nil {
		t.Fatalf("engine refused the lounge: %v", err)
	}
}

func TestListedLightMissingFromItsRoomIsRefused(t *testing.T) {
	c := DefaultConfig()
	c.Rooms = append(c.Rooms, RoomConfig{Name: "Lounge", Lights: []string{"f", "k"}}) // "k" is in the kitchen
	rooms, devices, lights := loungeHouse()
	if _, err := SelectLights(c, rooms, devices, lights); err == nil {
		t.Fatal("a listed light outside the lounge was accepted")
	}
}
