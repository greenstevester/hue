package circadian

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kungfusheep/hue/client"
)

func TestSessionDiscoversOnlyApprovedRoomsStartsPausedAndReleasesOwnership(t *testing.T) {
	var mu sync.Mutex
	writes := []string{}
	rooms := []client.Room{
		{ID: "kitchen", Metadata: client.Metadata{Name: "Kitchen"},
			Children: []client.ResourceIdentifier{{RID: "dk", RType: "device"}}},
		{ID: "bedroom", Metadata: client.Metadata{Name: "Bedroom"},
			Children: []client.ResourceIdentifier{{RID: "db", RType: "device"}}},
		{ID: "office", Metadata: client.Metadata{Name: "Office"},
			Children: []client.ResourceIdentifier{{RID: "do", RType: "device"}}},
	}
	var devices []client.Device
	var lights []client.Light
	for _, suffix := range []string{"k", "b", "o"} {
		devices = append(devices, client.Device{ID: "d" + suffix,
			Services: []client.ResourceIdentifier{{RID: suffix, RType: "light"}}})
		lights = append(lights, client.Light{ID: suffix, On: client.OnState{On: true},
			Dimming: client.Dimming{Brightness: 50},
			ColorTemperature: &client.ColorTemperature{Mirek: 300, MirekValid: true,
				MirekSchema: &client.MirekSchema{MirekMinimum: 153, MirekMaximum: 454}}})
	}
	refreshed := make(chan struct{}, 5)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/eventstream/clip/v2" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.Method == http.MethodPut {
			mu.Lock()
			writes = append(writes, r.URL.Path)
			mu.Unlock()
			fmt.Fprint(w, `{"errors":[],"data":[]}`)
			return
		}
		var data interface{}
		switch r.URL.Path {
		case "/clip/v2/resource/room":
			data = rooms
		case "/clip/v2/resource/device":
			data = devices
		case "/clip/v2/resource/light":
			data = lights
		case "/clip/v2/resource/entertainment_configuration":
			data = []client.Entertainment{}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"errors": []interface{}{}, "data": data})
		if r.URL.Path == "/clip/v2/resource/entertainment_configuration" {
			refreshed <- struct{}{}
		}
	}))
	defer server.Close()
	c := client.NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	config := DefaultConfig()
	config.Enabled = true
	engine, done, stop, err := Start(context.Background(), c, config, func(string, ...interface{}) {})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("initial refresh missing")
	}
	if !engine.Status().Paused || len(engine.Status().Lights) != 2 {
		t.Fatalf("%+v", engine.Status())
	}
	if _, _, _, err := Start(context.Background(), c, config, func(string, ...interface{}) {}); err == nil {
		t.Fatal("duplicate owner accepted")
	}
	stop()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(writes) != 0 {
		t.Fatalf("paused runner wrote %v", writes)
	}
	_, _, stopAgain, err := Start(context.Background(), c, config, func(string, ...interface{}) {})
	if err != nil {
		t.Fatalf("ownership not released: %v", err)
	}
	stopAgain()
}
