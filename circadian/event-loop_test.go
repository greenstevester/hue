package circadian

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kungfusheep/hue/client"
)

// eventLoopFixture runs the real event loop against a fake Bridge whose stream sends what the test pushes.
func eventLoopFixture(t *testing.T) (chan<- string, *Engine) {
	t.Helper()
	rooms := []client.Room{
		{ID: "kitchen", Metadata: client.Metadata{Name: "Kitchen"},
			Children: []client.ResourceIdentifier{{RID: "k", RType: "light"}}},
		{ID: "bedroom", Metadata: client.Metadata{Name: "Bedroom"},
			Children: []client.ResourceIdentifier{{RID: "b", RType: "light"}}},
	}
	lights := []client.Light{
		{ID: "k", On: client.OnState{On: true}, Dimming: client.Dimming{Brightness: 50}},
		{ID: "b", On: client.OnState{On: true}, Dimming: client.Dimming{Brightness: 50}},
	}
	pushed := make(chan string, 10)
	refreshed := make(chan struct{}, 10)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/eventstream/clip/v2" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			for {
				select {
				case <-r.Context().Done():
					return
				case data := <-pushed:
					fmt.Fprintf(w, "data: %s\n\n", data)
					w.(http.Flusher).Flush()
				}
			}
		}
		var data interface{}
		switch r.URL.Path {
		case "/clip/v2/resource/room":
			data = rooms
		case "/clip/v2/resource/device":
			data = []client.Device{}
		case "/clip/v2/resource/light":
			data = lights
		case "/clip/v2/resource/entertainment_configuration":
			data = []client.Entertainment{}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"errors": []interface{}{}, "data": data})
		if r.URL.Path == "/clip/v2/resource/entertainment_configuration" {
			refreshed <- struct{}{}
		}
	}))
	t.Cleanup(server.Close)
	c := client.NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	config := DefaultConfig()
	managed, err := RoomLights(config, rooms, nil, lights)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(config, c, managed, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, c, e, func(string, ...interface{}) {}, nil) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("first refresh never happened")
	}
	waitFor(t, func() bool { return e.Status().Healthy })
	return pushed, e
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within five seconds")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOnlyLightRoomAndDeviceChangesStopAutomationUntilRestart(t *testing.T) {
	tests := []struct {
		name       string
		event      string
		wantFrozen bool
	}{
		{"new scene", `{"type":"add","data":[{"id":"s1","type":"scene"}]}`, false},
		{"deleted zone", `{"type":"delete","data":[{"id":"z1","type":"zone"}]}`, false},
		{"new automation", `{"type":"add","data":[{"id":"a1","type":"behavior_instance"}]}`, false},
		{"new light", `{"type":"add","data":[{"id":"new","type":"light"}]}`, true},
		{"deleted light", `{"type":"delete","data":[{"id":"k","type":"light"}]}`, true},
		{"new device", `{"type":"add","data":[{"id":"d1","type":"device"}]}`, true},
		{"room membership update", `{"type":"update","data":[{"id":"kitchen","type":"room"}]}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pushed, e := eventLoopFixture(t)
			// The light update afterwards proves the loop has already handled the event under test.
			pushed <- "[" + test.event + `,{"type":"update","data":[{"id":"b","type":"light","dimming":{"brightness":20}}]}]`
			waitFor(t, func() bool {
				for _, light := range e.Status().Lights {
					if light.ID == "b" && light.Brightness == 20 {
						return true
					}
				}
				return false
			})
			err := e.SetEnabled(true)
			if test.wantFrozen && err == nil {
				t.Fatal("automation could be enabled after the managed lights changed")
			}
			if !test.wantFrozen && err != nil {
				t.Fatalf("unrelated Bridge change stopped automation: %v", err)
			}
		})
	}
}
