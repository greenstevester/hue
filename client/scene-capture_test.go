package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// sceneBridge serves a Kitchen room (one lamp in colour-temperature mode), a Reading zone (one lamp
// in colour mode), and groups owned by each and by the whole home. It records scene bodies posted.
func sceneBridge(t *testing.T) (*Client, func() []map[string]interface{}) {
	t.Helper()
	var mu sync.Mutex
	var posted []map[string]interface{}
	owner := func(id, kind string) *ResourceIdentifier { return &ResourceIdentifier{RID: id, RType: kind} }
	resources := map[string]interface{}{
		"room": []Room{{ID: "kitchen", Metadata: Metadata{Name: "Kitchen"},
			Children: []ResourceIdentifier{{RID: "dev-k", RType: "device"}}}},
		"zone": []Zone{{ID: "reading", Metadata: Metadata{Name: "Reading"},
			Children: []ResourceIdentifier{{RID: "r-lamp", RType: "light"}}},
			{ID: "hall", Metadata: Metadata{Name: "Hall"}, // its second lamp no longer exists
				Children: []ResourceIdentifier{{RID: "r-lamp", RType: "light"}, {RID: "gone", RType: "light"}}}},
		"device":                   []Device{{ID: "dev-k", Services: []ResourceIdentifier{{RID: "k-lamp", RType: "light"}}}},
		"grouped_light/gl-kitchen": []Group{{ID: "gl-kitchen", Owner: owner("kitchen", "room")}},
		"grouped_light/gl-reading": []Group{{ID: "gl-reading", Owner: owner("reading", "zone")}},
		"grouped_light/gl-home":    []Group{{ID: "gl-home", Owner: owner("home", "bridge_home")}},
		"light/k-lamp": []Light{{ID: "k-lamp", On: OnState{On: true}, Dimming: Dimming{Brightness: 60},
			Color:            &Color{XY: XY{X: .45, Y: .41}},
			ColorTemperature: &ColorTemperature{Mirek: 300, MirekValid: true}}},
		"light/r-lamp": []Light{{ID: "r-lamp", On: OnState{On: true}, Dimming: Dimming{Brightness: 20},
			Color:            &Color{XY: XY{X: .6, Y: .3}},
			ColorTemperature: &ColorTemperature{MirekValid: false}}},
		"scene/new-scene": []Scene{{ID: "new-scene"}},
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/clip/v2/resource/")
		if r.Method == http.MethodPost && path == "scene" {
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("scene body: %v", err)
			}
			mu.Lock()
			posted = append(posted, body)
			mu.Unlock()
			fmt.Fprint(w, `{"errors":[],"data":[{"rid":"new-scene","rtype":"scene"}]}`)
			return
		}
		data, ok := resources[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"description":"not found"}],"data":[]}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"errors": []interface{}{}, "data": data})
	}))
	t.Cleanup(server.Close)
	c := NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	return c, func() []map[string]interface{} { mu.Lock(); defer mu.Unlock(); return posted }
}

func TestSceneFromGroupBelongsToItsRoomAndCapturesLights(t *testing.T) {
	c, posted := sceneBridge(t)
	if _, err := c.CreateSceneFromCurrentState(context.Background(), "Evening", "gl-kitchen"); err != nil {
		t.Fatal(err)
	}
	body := posted()[0]
	group := body["group"].(map[string]interface{})
	if group["rid"] != "kitchen" || group["rtype"] != "room" {
		t.Fatalf("scene group %v, want the Kitchen room", group)
	}
	actions := body["actions"].([]interface{})
	if len(actions) != 1 {
		t.Fatalf("captured %d lights, want 1", len(actions))
	}
	action := actions[0].(map[string]interface{})["action"].(map[string]interface{})
	if action["dimming"] == nil || action["color_temperature"] == nil || action["color"] != nil {
		t.Fatalf("lamp in colour-temperature mode captured as %v", action)
	}
}

func TestSceneFromZoneCapturesColourModeLights(t *testing.T) {
	c, posted := sceneBridge(t)
	if _, err := c.CreateSceneFromCurrentState(context.Background(), "Read", "gl-reading"); err != nil {
		t.Fatal(err)
	}
	body := posted()[0]
	if group := body["group"].(map[string]interface{}); group["rid"] != "reading" || group["rtype"] != "zone" {
		t.Fatalf("scene group %v, want the Reading zone", group)
	}
	action := body["actions"].([]interface{})[0].(map[string]interface{})["action"].(map[string]interface{})
	if action["color"] == nil || action["color_temperature"] != nil {
		t.Fatalf("lamp in colour mode captured as %v", action)
	}
}

func TestSceneFromWholeHomeGroupIsRefused(t *testing.T) {
	c, posted := sceneBridge(t)
	_, err := c.CreateSceneFromCurrentState(context.Background(), "All", "gl-home")
	if err == nil || !strings.Contains(err.Error(), "does not belong to a room or zone") {
		t.Fatalf("got %v", err)
	}
	if len(posted()) != 0 {
		t.Fatal("scene sent to the Bridge anyway")
	}
}

func TestSceneIsNotCreatedWhenALightCannotBeRead(t *testing.T) {
	c, posted := sceneBridge(t)
	_, err := c.CreateSceneFromCurrentState(context.Background(), "Hall", "hall")
	if err == nil || !strings.Contains(err.Error(), "read light gone in zone hall") {
		t.Fatalf("got %v", err)
	}
	if len(posted()) != 0 {
		t.Fatal("scene missing a light was sent to the Bridge")
	}
}
