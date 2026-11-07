package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// Scene CRUD operations

// CreateSceneFromCurrentState creates a scene that captures the current state of every light in a
// room or zone. groupID may be a room or zone ID, or the ID of a group (grouped_light) that one owns.
func (c *Client) CreateSceneFromCurrentState(ctx context.Context, name string, groupID string) (*Scene, error) {
	group, lightIDs, err := c.sceneGroup(ctx, groupID, true)
	if err != nil {
		return nil, err
	}

	// Get current state of each light
	var actions []SceneAction
	for _, lightID := range lightIDs {
		light, err := c.GetLight(ctx, lightID)
		if err != nil {
			// A scene quietly missing a light is worse than no scene.
			return nil, fmt.Errorf("read light %s in %s %s: %w", lightID, group.RType, group.RID, err)
		}
		action := SceneAction{
			Target: ResourceIdentifier{RID: lightID, RType: "light"},
			Action: LightUpdate{On: &OnState{On: light.On.On}},
		}
		if light.On.On && light.Dimming.Brightness > 0 {
			action.Action.Dimming = &Dimming{Brightness: light.Dimming.Brightness}
		}
		// A light reports both colour values; send only the one for the mode it is in.
		if light.ColorTemperature != nil && light.ColorTemperature.MirekValid {
			action.Action.ColorTemperature = &ColorTemperature{Mirek: light.ColorTemperature.Mirek}
		} else if light.Color != nil {
			action.Action.Color = &Color{XY: light.Color.XY}
		}
		actions = append(actions, action)
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("%s %s has no lights to capture", group.RType, group.RID)
	}

	return c.CreateScene(ctx, SceneCreate{
		Type:     "scene",
		Metadata: Metadata{Name: name},
		Group:    group,
		Actions:  actions,
		Speed:    0.5, // Default transition speed
	})
}

// sceneGroup finds the room or zone a scene belongs to and the lights in it. Scenes cannot belong to
// a group (grouped_light), so a group ID is followed to the room or zone that owns it.
func (c *Client) sceneGroup(ctx context.Context, id string, followOwner bool) (ResourceIdentifier, []string, error) {
	var group ResourceIdentifier
	var children []ResourceIdentifier
	rooms, err := c.GetRooms(ctx)
	if err != nil {
		return group, nil, fmt.Errorf("failed to get rooms: %w", err)
	}
	for _, room := range rooms {
		if room.ID == id {
			group, children = ResourceIdentifier{RID: id, RType: "room"}, room.Children
		}
	}
	if group.RID == "" {
		zones, err := c.GetZones(ctx)
		if err != nil {
			return group, nil, fmt.Errorf("failed to get zones: %w", err)
		}
		for _, zone := range zones {
			if zone.ID == id {
				group, children = ResourceIdentifier{RID: id, RType: "zone"}, zone.Children
			}
		}
	}
	if group.RID == "" {
		if !followOwner {
			return group, nil, fmt.Errorf("no room or zone with ID %s", id)
		}
		owned, err := c.GetGroup(ctx, id)
		if err != nil {
			return group, nil, fmt.Errorf("no room, zone or group with ID %s: %w", id, err)
		}
		if owned.Owner == nil || (owned.Owner.RType != "room" && owned.Owner.RType != "zone") {
			return group, nil, fmt.Errorf("group %s does not belong to a room or zone", id)
		}
		return c.sceneGroup(ctx, owned.Owner.RID, false)
	}

	devices, err := c.GetDevices(ctx)
	if err != nil {
		return group, nil, fmt.Errorf("failed to get devices: %w", err)
	}
	var lightIDs []string
	for _, child := range children {
		if child.RType == "light" {
			lightIDs = append(lightIDs, child.RID)
		}
		if child.RType == "device" {
			for _, device := range devices {
				if device.ID != child.RID {
					continue
				}
				for _, service := range device.Services {
					if service.RType == "light" {
						lightIDs = append(lightIDs, service.RID)
					}
				}
			}
		}
	}
	return group, lightIDs, nil
}

// DeleteScene deletes a scene
func (c *Client) DeleteScene(ctx context.Context, id string) error {
	_, err := c.delete(ctx, fmt.Sprintf("/resource/scene/%s", id))
	return err
}

// UpdateScene updates a scene's metadata
func (c *Client) UpdateScene(ctx context.Context, id string, update SceneUpdate) error {
	_, err := c.put(ctx, fmt.Sprintf("/resource/scene/%s", id), update)
	return err
}

// Group CRUD operations

// AddLightToGroup adds a light to a group
func (c *Client) AddLightToGroup(ctx context.Context, groupID, lightID string) error {
	// First get the current group
	groups, err := c.GetGroups(ctx)
	if err != nil {
		return err
	}

	var group *Group
	for _, g := range groups {
		if g.ID == groupID {
			group = &g
			break
		}
	}

	if group == nil {
		return fmt.Errorf("group %s not found", groupID)
	}

	// Groups in v2 are managed through rooms/zones
	// We need to find the room/zone and update its children
	rooms, err := c.GetRooms(ctx)
	if err != nil {
		return err
	}

	for _, room := range rooms {
		for _, service := range room.Services {
			if service.RType == "grouped_light" && service.RID == groupID {
				// This is the room for our group
				// Find the device that contains the light
				devices, err := c.GetDevices(ctx)
				if err != nil {
					return err
				}

				var deviceID string
				for _, device := range devices {
					for _, svc := range device.Services {
						if svc.RType == "light" && svc.RID == lightID {
							deviceID = device.ID
							break
						}
					}
					if deviceID != "" {
						break
					}
				}

				if deviceID == "" {
					return fmt.Errorf("device containing light %s not found", lightID)
				}

				// Check if device is already in room
				for _, child := range room.Children {
					if child.RType == "device" && child.RID == deviceID {
						return nil // Already in the room
					}
				}

				// Add device to room
				room.Children = append(room.Children, ResourceIdentifier{
					RID:   deviceID,
					RType: "device",
				})

				// Update room
				update := RoomUpdate{
					Children: room.Children,
				}

				return c.UpdateRoom(ctx, room.ID, update)
			}
		}
	}

	// Check zones too
	zones, err := c.GetZones(ctx)
	if err != nil {
		return err
	}

	for _, zone := range zones {
		for _, service := range zone.Services {
			if service.RType == "grouped_light" && service.RID == groupID {
				// Add light directly to zone
				zone.Children = append(zone.Children, ResourceIdentifier{
					RID:   lightID,
					RType: "light",
				})

				// Update zone
				update := ZoneUpdate{
					Children: zone.Children,
				}

				return c.UpdateZone(ctx, zone.ID, update)
			}
		}
	}

	return fmt.Errorf("room or zone for group %s not found", groupID)
}

// RemoveLightFromGroup removes a light from a group
func (c *Client) RemoveLightFromGroup(ctx context.Context, groupID, lightID string) error {
	// Similar to AddLightToGroup but removes the device/light
	rooms, err := c.GetRooms(ctx)
	if err != nil {
		return err
	}

	for _, room := range rooms {
		for _, service := range room.Services {
			if service.RType == "grouped_light" && service.RID == groupID {
				// Find device containing the light
				devices, err := c.GetDevices(ctx)
				if err != nil {
					return err
				}

				var deviceID string
				for _, device := range devices {
					for _, svc := range device.Services {
						if svc.RType == "light" && svc.RID == lightID {
							deviceID = device.ID
							break
						}
					}
					if deviceID != "" {
						break
					}
				}

				// Remove device from room children
				var newChildren []ResourceIdentifier
				for _, child := range room.Children {
					if !(child.RType == "device" && child.RID == deviceID) {
						newChildren = append(newChildren, child)
					}
				}

				if len(newChildren) == len(room.Children) {
					return nil // Device wasn't in the room
				}

				// Update room
				update := RoomUpdate{
					Children: newChildren,
				}

				return c.UpdateRoom(ctx, room.ID, update)
			}
		}
	}

	// Check zones
	zones, err := c.GetZones(ctx)
	if err != nil {
		return err
	}

	for _, zone := range zones {
		for _, service := range zone.Services {
			if service.RType == "grouped_light" && service.RID == groupID {
				// Remove light from zone children
				var newChildren []ResourceIdentifier
				for _, child := range zone.Children {
					if !(child.RType == "light" && child.RID == lightID) {
						newChildren = append(newChildren, child)
					}
				}

				if len(newChildren) == len(zone.Children) {
					return nil // Light wasn't in the zone
				}

				// Update zone
				update := ZoneUpdate{
					Children: newChildren,
				}

				return c.UpdateZone(ctx, zone.ID, update)
			}
		}
	}

	return fmt.Errorf("room or zone for group %s not found", groupID)
}

// Room/Zone update operations

// UpdateRoom updates a room
func (c *Client) UpdateRoom(ctx context.Context, id string, update RoomUpdate) error {
	_, err := c.put(ctx, fmt.Sprintf("/resource/room/%s", id), update)
	return err
}

// UpdateZone updates a zone
func (c *Client) UpdateZone(ctx context.Context, id string, update ZoneUpdate) error {
	_, err := c.put(ctx, fmt.Sprintf("/resource/zone/%s", id), update)
	return err
}

// CreateZone creates a new zone
func (c *Client) CreateZone(ctx context.Context, zone ZoneCreate) (*Zone, error) {
	var response struct {
		Data   []Zone  `json:"data"`
		Errors []Error `json:"errors"`
	}

	respBody, err := c.post(ctx, "/resource/zone", zone)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(response.Errors) > 0 {
		return nil, fmt.Errorf("API error: %s", response.Errors[0].Description)
	}

	if len(response.Data) == 0 {
		return nil, fmt.Errorf("no zone returned")
	}

	return &response.Data[0], nil
}

// DeleteZone deletes a zone
func (c *Client) DeleteZone(ctx context.Context, id string) error {
	_, err := c.delete(ctx, fmt.Sprintf("/resource/zone/%s", id))
	return err
}
