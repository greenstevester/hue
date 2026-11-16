package circadian

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kungfusheep/hue/client"
)

// eventLoopSkew moves the fixture's clock ahead, so a test can bring the next room check forward.
var eventLoopSkew atomic.Int64

// eventLoopWrites counts the light updates the fixture's Bridge receives.
var eventLoopWrites atomic.Int64

// eventLoopDuringCheck, when set, is sent down the event stream while the fixture's Bridge answers
// the next room check, as if the rooms changed just after the Bridge replied.
var eventLoopDuringCheck atomic.Pointer[string]

// eventLoopFixture runs the real event loop against a fake Bridge whose stream sends what the test
// pushes. Sending on the returned ticks channel stands in for the loop's one-second clock.
func eventLoopFixture(t *testing.T) (chan<- string, chan<- time.Time, *Engine) {
	t.Helper()
	rooms := []client.Room{
		{ID: "kitchen", Metadata: client.Metadata{Name: "Kitchen"},
			Children: []client.ResourceIdentifier{{RID: "k", RType: "light"}}},
		{ID: "bedroom", Metadata: client.Metadata{Name: "Bedroom"},
			Children: []client.ResourceIdentifier{{RID: "b", RType: "light"}}},
	}
	lights := []client.Light{
		{ID: "k", On: client.OnState{On: true}, Dimming: client.Dimming{Brightness: 50}},
		// "b" can show colour, so no phase makes the schedule switch it off.
		{ID: "b", On: client.OnState{On: true}, Dimming: client.Dimming{Brightness: 50}, Color: &client.Color{}},
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
		if r.Method == http.MethodPut {
			eventLoopWrites.Add(1)
			_, _ = fmt.Fprint(w, `{"errors":[],"data":[]}`)
			return
		}
		var data interface{}
		switch r.URL.Path {
		case "/clip/v2/resource/room":
			data = rooms
			if event := eventLoopDuringCheck.Swap(nil); event != nil {
				pushed <- *event
			}
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
	t.Cleanup(func() {
		eventLoopSkew.Store(0)
		eventLoopWrites.Store(0)
		eventLoopDuringCheck.Store(nil)
	})
	clock := func() time.Time { return time.Now().Add(time.Duration(eventLoopSkew.Load())) }
	e, err := NewEngine(config, c, managed, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ticks := make(chan time.Time)
	go func() { done <- run(ctx, c, e, func(string, ...interface{}) {}, ticks) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("stopping the event loop was reported as a failure: %v", err)
		}
	})
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("first refresh never happened")
	}
	waitFor(t, func() bool { return e.Status().Healthy })
	return pushed, ticks, e
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

func TestOnlyLightRoomAndDeviceChangesStopAutomationUntilTheRoomsAreChecked(t *testing.T) {
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
			pushed, _, e := eventLoopFixture(t)
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

func TestBridgeRestartEventsStopAutomationOnlyUntilTheNextRoomCheck(t *testing.T) {
	pushed, ticks, e := eventLoopFixture(t)
	// What a restarting Bridge sends as it comes back, while its rooms stay as they were.
	pushed <- `[{"type":"update","data":[{"id":"kitchen","type":"room"},` +
		`{"id":"d1","type":"device"}]},` +
		`{"type":"update","data":[{"id":"b","type":"light","dimming":{"brightness":20}}]}]`
	waitFor(t, func() bool {
		for _, light := range e.Status().Lights {
			if light.ID == "b" && light.Brightness == 20 {
				return true
			}
		}
		return false
	})
	if err := e.SetEnabled(true); err == nil {
		t.Fatal("automation could be enabled before the rooms were checked")
	}
	eventLoopSkew.Store(int64(31 * time.Second)) // the next tick is due a room check
	ticks <- time.Now()
	waitFor(t, func() bool { return e.SetEnabled(true) == nil })
}

func TestRoomChangeDuringTheRoomCheckIsHandledBeforeAnythingIsWritten(t *testing.T) {
	pushed, ticks, e := eventLoopFixture(t)
	if err := e.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	pushed <- `[{"type":"update","data":[{"id":"kitchen","type":"room"}]}]`
	waitFor(t, func() bool { return e.Status().Paused })
	// The Bridge answers the check with the old rooms, then reports a change straight after.
	// The light item that follows shows when the loop has handled the change.
	during := `[{"type":"update","data":[{"id":"kitchen","type":"room"}]},` +
		`{"type":"update","data":[{"id":"b","type":"light","dimming":{"brightness":20}}]}]`
	eventLoopDuringCheck.Store(&during)
	eventLoopSkew.Store(int64(31 * time.Second)) // the next tick is due a room check
	ticks <- time.Now()
	waitFor(t, func() bool {
		for _, light := range e.Status().Lights {
			if light.ID == "b" && light.Brightness == 20 {
				return true
			}
		}
		return false
	})
	if writes := eventLoopWrites.Load(); writes != 0 {
		t.Fatalf("%d light updates went out before the change that arrived during the check", writes)
	}
	if !e.Status().Paused {
		t.Fatal("the change that arrived during the check did not stop automation")
	}
}

func TestStoppingTheEventLoopIsNotAFailure(t *testing.T) {
	// A stop signal often lands while the loop is busy, for example mid-refresh. By the time the
	// loop looks again, the event stream has closed too, and Go picks between "cancelled" and
	// "closed" at random, so run it several times.
	for i := 0; i < 25; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			pushed, _, e := eventLoopFixture(t)
			e.mu.Lock() // the loop stalls in Observe until the stream has closed
			pushed <- `[{"type":"update","data":[{"id":"b","type":"light","dimming":{"brightness":20}}]}]`
			time.Sleep(50 * time.Millisecond) // let the loop pick up the event and block on the lock
			// Cleanups run last-registered first: this releases the lock 200 ms after the
			// fixture's own cleanup has cancelled the loop.
			t.Cleanup(func() { time.AfterFunc(200*time.Millisecond, e.mu.Unlock) })
		})
	}
}

func TestSwitchOnWithItsOwnBrightnessHoldsWhateverOrderTheBridgeSendsIt(t *testing.T) {
	// The Bridge reports one change as separate items in one event; on 9 November a light switched
	// on at 40% arrived as {on}, {dimming}, {color}. Read alone, the "on" item looks like a plain
	// switch-on, which would hand the light back to the schedule.
	for name, items := range map[string]string{
		"on first":  `{"id":"b","type":"light","on":{"on":true}},{"id":"b","type":"light","dimming":{"brightness":20}}`,
		"on second": `{"id":"b","type":"light","dimming":{"brightness":20}},{"id":"b","type":"light","on":{"on":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			pushed, _, e := eventLoopFixture(t)
			pushed <- `[{"type":"update","data":[{"id":"b","type":"light","on":{"on":false}}]}]`
			pushed <- `[{"type":"update","data":[` + items + `]}]`
			var light LightStatus
			waitFor(t, func() bool {
				for _, light = range e.Status().Lights {
					if light.ID == "b" && light.On && light.Brightness == 20 {
						return true
					}
				}
				return false
			})
			if light.HoldUntil.IsZero() {
				t.Fatal("light switched on at a brightness of its own was handed back to the schedule")
			}
		})
	}
}
