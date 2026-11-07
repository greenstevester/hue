package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kungfusheep/hue/client"
)

// sceneBridge serves two "Relax" scenes, in the Living room and the Bedroom, and one "Read" scene.
func sceneBridge(t *testing.T) {
	t.Helper()
	scene := func(id, name, room string) client.Scene {
		return client.Scene{ID: id, Metadata: client.Metadata{Name: name},
			Group: client.ResourceIdentifier{RID: room, RType: "room"}}
	}
	data := map[string]interface{}{
		"/clip/v2/resource/scene": []client.Scene{
			scene("relax-living", "Relax", "living"), scene("relax-bedroom", "Relax", "bedroom"),
			scene("read-living", "Read", "living"),
		},
		"/clip/v2/resource/room": []client.Room{
			{ID: "living", Metadata: client.Metadata{Name: "Living room"}},
			{ID: "bedroom", Metadata: client.Metadata{Name: "Bedroom"}},
			{ID: "kitchen", Metadata: client.Metadata{Name: "Kitchen"}},
		},
		"/clip/v2/resource/zone": []client.Zone{},
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := data[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"errors": []interface{}{}, "data": body})
	}))
	t.Cleanup(server.Close)
	previous := hueClient
	hueClient = client.NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	t.Cleanup(func() { hueClient = previous })
}

func TestSceneRoomFilterPicksTheScenesRoom(t *testing.T) {
	sceneBridge(t)
	id, err := resolveSceneID(context.Background(), "Relax:Bedroom")
	if err != nil || id != "relax-bedroom" {
		t.Fatalf("got %q, %v", id, err)
	}
}

func TestSceneRoomFilterWithoutMatchRefusesOtherRooms(t *testing.T) {
	sceneBridge(t)
	for _, input := range []string{"Read:Kitchen", "Read:Kitchn", "Relax:Kitchen"} {
		id, err := resolveSceneID(context.Background(), input)
		if err == nil {
			t.Fatalf("%s activated %q from another room", input, id)
		}
		if !strings.Contains(err.Error(), "Living room") {
			t.Fatalf("%s: error does not say where the scene exists: %v", input, err)
		}
	}
}

func TestSceneRoomFilterForUnknownSceneSaysSo(t *testing.T) {
	sceneBridge(t)
	_, err := resolveSceneID(context.Background(), "Party:Kitchen")
	if err == nil || !strings.Contains(err.Error(), `no scene named "Party"`) {
		t.Fatalf("got %v", err)
	}
}
