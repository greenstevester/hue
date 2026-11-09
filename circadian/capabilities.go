package circadian

import (
	"fmt"
	"math"
	"strings"

	"github.com/kungfusheep/hue/client"
)

type ManagedLight struct {
	Light client.Light `json:"light"`
	Room  RoomConfig   `json:"room"`
}

// RoomLights includes excluded lights for observation and the web interface.
func RoomLights(c Config, rooms []client.Room, devices []client.Device, lights []client.Light) ([]ManagedLight, error) {
	c.ExcludeLights = nil
	return SelectLights(c, rooms, devices, lights)
}

// SelectLights fails closed unless every configured room resolves exactly and unambiguously.
func SelectLights(
	c Config, rooms []client.Room, devices []client.Device, lights []client.Light,
) ([]ManagedLight, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	excluded := map[string]bool{}
	for _, id := range c.ExcludeLights {
		excluded[id] = true
	}
	var result []ManagedLight
	selected := map[string]bool{}
	for _, wanted := range c.Rooms {
		var matches []client.Room
		for _, room := range rooms {
			if strings.EqualFold(room.Metadata.Name, wanted.Name) && (wanted.ID == "" || wanted.ID == room.ID) {
				matches = append(matches, room)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("room %s has %d exact matches; refusing control", wanted.Name, len(matches))
		}
		members := map[string]bool{}
		for _, child := range matches[0].Children {
			if child.RType == "light" {
				members[child.RID] = true
			}
			if child.RType == "device" {
				for _, device := range devices {
					if device.ID == child.RID {
						for _, service := range device.Services {
							if service.RType == "light" {
								members[service.RID] = true
							}
						}
					}
				}
			}
		}
		listed := map[string]bool{}
		for _, id := range wanted.Lights {
			listed[id] = true
		}
		count, found := 0, 0
		for _, light := range lights {
			if members[light.ID] && listed[light.ID] {
				found++
			}
			if !members[light.ID] || excluded[light.ID] || (len(listed) > 0 && !listed[light.ID]) {
				continue
			}
			if selected[light.ID] {
				return nil, fmt.Errorf("light %s belongs to both selected rooms", light.ID)
			}
			selected[light.ID] = true
			result = append(result, ManagedLight{Light: light, Room: wanted})
			count++
		}
		if found != len(listed) {
			return nil, fmt.Errorf("room %s does not contain every listed light; refusing control", wanted.Name)
		}
		if count == 0 {
			return nil, fmt.Errorf("room %s has no eligible lights", wanted.Name)
		}
	}
	return result, nil
}

// Adapt produces a device-safe target, preserving the user's off state.
func Adapt(t Target, l client.Light, switchedOff bool) client.LightUpdate {
	// ponytail: Gradient lights receive a uniform colour; add per-segment output only if requested.
	on := l.On.On || switchedOff
	update := client.LightUpdate{}
	if !on {
		return update
	}
	if t.Brightness <= 0 || (t.ColorOnly && l.Color == nil) {
		update.On = &client.OnState{On: false}
		return update
	}
	if switchedOff {
		update.On = &client.OnState{On: true}
	}
	if l.HasDimming {
		update.Dimming = &client.Dimming{Brightness: math.Max(l.Dimming.MinDimLevel, math.Min(100, t.Brightness))}
	}
	if (t.XY == nil || l.Color == nil && !t.ColorOnly) && t.Mirek > 0 &&
		l.ColorTemperature != nil && l.ColorTemperature.MirekSchema != nil {
		limits := l.ColorTemperature.MirekSchema
		if limits.MirekMinimum > 0 && limits.MirekMaximum >= limits.MirekMinimum {
			mirek := int(math.Round(t.Mirek))
			mirek = max(limits.MirekMinimum, min(limits.MirekMaximum, mirek))
			update.ColorTemperature = &client.ColorTemperature{Mirek: mirek}
		}
	} else if l.Color != nil {
		update.Color = &client.Color{XY: client.ClipXY(targetXY(t), l.Color.Gamut)}
	}
	return update
}
