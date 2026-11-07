package webui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kungfusheep/hue/circadian"
)

type fakeController struct {
	mu     sync.Mutex
	state  circadian.Status
	reject bool
}

func (f *fakeController) Status() circadian.Status { f.mu.Lock(); defer f.mu.Unlock(); return f.state }
func (f *fakeController) SetEnabled(enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reject && enabled {
		return fmt.Errorf("room membership changed")
	}
	f.state.Enabled = enabled
	f.state.Paused = !enabled
	return nil
}

func (f *fakeController) SetSchedule(wake, bedtime string) error { return nil }
func (f *fakeController) SetLightEnabled(id string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.state.Lights {
		if f.state.Lights[i].ID == id {
			f.state.Lights[i].Enabled = enabled
			return nil
		}
	}
	return fmt.Errorf("light outside managed rooms")
}

func fixture(t *testing.T) (*Server, *fakeController, string) {
	t.Helper()
	config := circadian.DefaultConfig()
	config.Enabled = true
	latitude, longitude := 47.3769, 8.5417
	config.Latitude = &latitude
	config.Longitude = &longitude
	config.WakeTime = "sunrise"
	path := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	controller := &fakeController{state: circadian.Status{Healthy: true, Enabled: true, Lights: []circadian.LightStatus{
		{ID: "k", Name: "Kitchen-Island", Room: "Kitchen", On: true, Brightness: 50, Enabled: true,
			Target: circadian.Target{Phase: "late_afternoon"}},
		{ID: "b", Name: "bedroom-window", Room: "Bedroom", On: false, Enabled: true},
	}}}
	now := time.Date(2026, 11, 7, 14, 30, 0, 0, time.UTC)
	s, err := New(controller, config, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return s, controller, path
}

func request(s http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestStatusShowsObservedLightsAndZurichSchedule(t *testing.T) {
	s, _, _ := fixture(t)
	response := request(s, "GET", "/api/status", "")
	var state snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || !state.Enabled || len(state.Lights) != 2 || len(state.Schedule) != 6 {
		t.Fatalf("unexpected status: %s", response.Body.String())
	}
	if state.Lights[1].Name != "Kitchen-Island" || state.Lights[1].Brightness != 50 {
		t.Fatal("observed light state lost")
	}
	if state.WakeTime != "sunrise" || state.Timezone != "Europe/Zurich" || state.Schedule[0].At.Hour() != 7 {
		t.Fatalf("wrong schedule: %+v", state.Schedule)
	}
	if strings.Contains(response.Body.String(), "HUE_USERNAME") {
		t.Fatal("credential exposed")
	}
}

func TestEnableDisablePersistsWithoutChangingProfiles(t *testing.T) {
	s, controller, path := fixture(t)
	for _, enabled := range []bool{false, true, false} {
		response := request(s, "POST", "/api/control", fmt.Sprintf(`{"enabled":%t}`, enabled))
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		saved, err := circadian.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Enabled != enabled || controller.Status().Enabled != enabled {
			t.Fatal("state was not persisted")
		}
		if saved.WakeTime != "sunrise" || saved.Profiles["morning"].Kelvin != 4000 {
			t.Fatal("profile settings changed")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("private configuration permissions changed")
	}
	config, err := circadian.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &fakeController{state: circadian.Status{Enabled: config.Enabled}}
	if _, err := New(restarted, config, path, time.Now); err != nil {
		t.Fatal(err)
	}
	if restarted.Status().Enabled {
		t.Fatal("disabled state lost after restart")
	}
}

func TestControlRejectsCrossSiteAndMalformedRequests(t *testing.T) {
	tests := []struct {
		name, body, origin, contentType, host string
		want                                  int
	}{
		{name: "cross origin", body: `{"enabled":false}`, origin: "https://attacker.example", want: 403},
		{name: "unknown host", body: `{"enabled":false}`, host: "attacker.example", want: 403},
		{name: "missing field", body: `{}`, want: 400},
		{name: "null", body: `{"enabled":null}`, want: 400},
		{name: "wrong type", body: `{"enabled":"false"}`, want: 400},
		{name: "unknown field", body: `{"enabled":false,"room":"Office"}`, want: 400},
		{name: "two bodies", body: `{"enabled":false} {}`, want: 400},
		{name: "too large", body: `{"enabled":false}` + strings.Repeat(" ", 1100), want: 400},
		{name: "form", body: "enabled=false", contentType: "application/x-www-form-urlencoded", want: 415},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, controller, _ := fixture(t)
			r := httptest.NewRequest("POST", "http://localhost/api/control", strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			if test.contentType != "" {
				r.Header.Set("Content-Type", test.contentType)
			}
			if test.origin != "" {
				r.Header.Set("Origin", test.origin)
			}
			if test.host != "" {
				r.Host = test.host
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if !controller.Status().Enabled {
				t.Fatal("rejected request changed automation")
			}
		})
	}
}

func TestFailedSaveRestoresRuntimeState(t *testing.T) {
	s, controller, path := fixture(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	response := request(s, "POST", "/api/control", `{"enabled":false}`)
	if response.Code != 500 || !controller.Status().Enabled {
		t.Fatal("failed save left runtime state changed")
	}
}

func TestChangedRoomInventoryPreventsEnable(t *testing.T) {
	s, controller, _ := fixture(t)
	controller.state.Enabled = false
	controller.reject = true
	response := request(s, "POST", "/api/control", `{"enabled":true}`)
	if response.Code != 409 || controller.Status().Enabled {
		t.Fatal("invalid inventory enabled")
	}
}

func TestEmbeddedPageWorksWithoutExternalAssets(t *testing.T) {
	s, _, _ := fixture(t)
	for _, path := range []string{"/", "/style.css", "/app.js"} {
		response := request(s, "GET", path, "")
		if response.Code != 200 {
			t.Fatalf("%s: %d", path, response.Code)
		}
		if !strings.Contains(response.Header().Get("Content-Security-Policy"), "'self'") {
			t.Fatal("missing content policy")
		}
	}
	page := request(s, "GET", "/", "").Body.String()
	if !strings.Contains(page, `role="switch"`) || !strings.Contains(page, "Today’s timing") {
		t.Fatal("missing primary controls")
	}
	if strings.Contains(page, "https://") {
		t.Fatal("page relies on an external resource")
	}
	if got := request(s, "GET", "/api/control", "").Code; got != 405 {
		t.Fatalf("GET changed state: %d", got)
	}
}

func TestConcurrentTogglesPersistTheFinalState(t *testing.T) {
	s, controller, path := fixture(t)
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func(enabled bool) {
			defer group.Done()
			response := request(s, "POST", "/api/control", fmt.Sprintf(`{"enabled":%t}`, enabled))
			if response.Code != 200 {
				t.Errorf("toggle failed: %s", response.Body.String())
			}
			_ = request(s, "GET", "/api/status", "")
		}(i%2 == 0)
	}
	group.Wait()
	saved, err := circadian.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Enabled != controller.Status().Enabled {
		t.Fatal("saved state differs from runtime state")
	}
}

func TestWakeAndBedtimeOverridesPersistAndRecalculateTiming(t *testing.T) {
	s, _, path := fixture(t)
	response := request(s, "POST", "/api/schedule", `{"wake_time":"08:00","bedtime":"23:00"}`)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var state snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.WakeTime != "08:00" || state.Bedtime != "23:00" || state.Schedule[0].At.Hour() != 8 {
		t.Fatal("schedule did not follow the saved override")
	}
	saved, err := circadian.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.WakeTime != "08:00" || saved.Bedtime != "23:00" {
		t.Fatal("timing override not persisted")
	}
	response = request(s, "POST", "/api/schedule", `{"wake_time":"sunrise","bedtime":"22:30"}`)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	saved, err = circadian.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.WakeTime != "sunrise" {
		t.Fatal("sunrise mode not restored")
	}
}

func TestInvalidTimingDoesNotChangeSavedSettings(t *testing.T) {
	s, _, path := fixture(t)
	response := request(s, "POST", "/api/schedule", `{"wake_time":"26:00","bedtime":"22:30"}`)
	if response.Code != 400 {
		t.Fatalf("%d: %s", response.Code, response.Body.String())
	}
	saved, err := circadian.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.WakeTime != "sunrise" {
		t.Fatal("invalid wake override changed configuration")
	}
}

func TestIndividualLightExclusionPersistsAndCanBeReenabled(t *testing.T) {
	s, controller, path := fixture(t)
	response := request(s, "POST", "/api/light", `{"id":"k","enabled":false}`)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	saved, err := circadian.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.ExcludeLights) != 1 || saved.ExcludeLights[0] != "k" {
		t.Fatal("light exclusion not saved")
	}
	state := controller.Status()
	if state.Lights[0].Enabled || !state.Lights[0].On || state.Lights[0].Brightness != 50 {
		t.Fatal("excluding a light changed its physical state")
	}
	response = request(s, "POST", "/api/light", `{"id":"k","enabled":true}`)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	saved, err = circadian.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.ExcludeLights) != 0 {
		t.Fatal("light was not reenabled")
	}
	response = request(s, "POST", "/api/light", `{"id":"office","enabled":true}`)
	if response.Code != 409 {
		t.Fatal("unmanaged light was accepted")
	}
}

func TestHostCheckFollowsAddressChangesWithoutRestart(t *testing.T) {
	s, _, _ := fixture(t)
	current := []net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.126"), Mask: net.CIDRMask(24, 32)}}
	s.addresses = func() ([]net.Addr, error) { return current, nil }
	get := func(host string) int {
		r := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/status", nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	if code := get("10.0.0.126:8088"); code != http.StatusOK {
		t.Fatalf("current address refused: %d", code)
	}
	current = []net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.140"), Mask: net.CIDRMask(24, 32)}}
	if code := get("10.0.0.140:8088"); code != http.StatusOK {
		t.Fatalf("new address from the router refused: %d", code)
	}
	if code := get("10.0.0.126:8088"); code != http.StatusForbidden {
		t.Fatalf("old address still accepted: %d", code)
	}
	if code := get("lights.example.com"); code != http.StatusForbidden {
		t.Fatalf("outside hostname accepted: %d", code)
	}
}
