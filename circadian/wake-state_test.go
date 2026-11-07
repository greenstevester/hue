package circadian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kungfusheep/hue/client"
)

// whiteBulbEngine runs the schedule for one white-only kitchen bulb ("k") at a fixed time.
func whiteBulbEngine(t *testing.T, at string, on bool) (*Engine, *fakeBridge) {
	t.Helper()
	c := DefaultConfig()
	c.Enabled = true
	now := instant(t, at)
	b := &fakeBridge{}
	l := client.Light{ID: "k", On: client.OnState{On: on}, HasDimming: true, Dimming: client.Dimming{Brightness: 50},
		ColorTemperature: &client.ColorTemperature{Mirek: 300, MirekValid: true,
			MirekSchema: &client.MirekSchema{MirekMinimum: 153, MirekMaximum: 454}}}
	e, err := NewEngine(c, b, []ManagedLight{{Light: l, Room: c.Rooms[0]}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	b.guard = e.Guard
	return e, b
}

func start(t *testing.T, e *Engine) {
	t.Helper()
	e.SetHealthy(true)
	e.Resume()
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// switchOffAtNight lets the evening phase switch the white-only bulb off and saves that to path.
func switchOffAtNight(t *testing.T, path string) *Engine {
	t.Helper()
	e, b := whiteBulbEngine(t, "2026-01-10T20:00:00+01:00", true)
	if err := e.RememberSwitchedOff(path); err != nil {
		t.Fatal(err)
	}
	start(t, e)
	if len(b.writes) != 1 || b.writes[0].On == nil || b.writes[0].On.On {
		t.Fatalf("evening did not switch the white-only bulb off: %+v", b.writes)
	}
	if err := e.saveSwitchedOff(); err != nil {
		t.Fatal(err)
	}
	return e
}

func savedSwitchOffs(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(data)), " ")
}

func TestLightSwitchedOffAtNightComesBackAtWakeAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.state.json")
	switchOffAtNight(t, path)

	restarted, b := whiteBulbEngine(t, "2026-01-11T07:45:00+01:00", false)
	if err := restarted.RememberSwitchedOff(path); err != nil {
		t.Fatal(err)
	}
	start(t, restarted)
	if len(b.writes) != 1 || b.writes[0].On == nil || !b.writes[0].On.On {
		t.Fatalf("bulb switched off by the schedule stayed off at wake after a restart: %+v", b.writes)
	}

	forgetful, b := whiteBulbEngine(t, "2026-01-11T07:45:00+01:00", false)
	start(t, forgetful)
	if len(b.writes) != 0 {
		t.Fatal("without the saved list, a bulb that is off must stay off")
	}
}

func TestStateFileIsPrivateAndListsTheSwitchedOffLight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.state.json")
	switchOffAtNight(t, path)
	if got := savedSwitchOffs(t, path); got != `{ "switched_off": [ "k" ] }` {
		t.Fatalf("saved %s", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("state file mode %v, want 0600", info.Mode().Perm())
	}
}

func TestManualChangeForgetsTheSwitchOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.state.json")
	e := switchOffAtNight(t, path)
	e.Observe(client.EventData{ID: "k", Type: "light", On: &client.OnState{On: false}}) // the Bridge reports the switch-off
	e.Observe(client.EventData{ID: "k", Type: "light", On: &client.OnState{On: true}})  // then someone turns it on
	if err := e.saveSwitchedOff(); err != nil {
		t.Fatal(err)
	}
	if got := savedSwitchOffs(t, path); got != `{ "switched_off": [] }` {
		t.Fatalf("light someone turned on is still saved as switched off by the schedule: %s", got)
	}
}

func TestSavedLightThatIsOnAgainIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.state.json")
	switchOffAtNight(t, path)
	restarted, b := whiteBulbEngine(t, "2026-01-11T07:45:00+01:00", true) // someone switched it on overnight
	if err := restarted.RememberSwitchedOff(path); err != nil {
		t.Fatal(err)
	}
	if err := restarted.saveSwitchedOff(); err != nil {
		t.Fatal(err)
	}
	if got := savedSwitchOffs(t, path); got != `{ "switched_off": [] }` {
		t.Fatalf("saved %s", got)
	}
	start(t, restarted)
	if len(b.writes) != 1 || b.writes[0].On != nil {
		t.Fatalf("light already on should just follow the schedule: %+v", b.writes)
	}
}

func TestUnreadableStateFileIsReportedAndReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.state.json")
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	e, _ := whiteBulbEngine(t, "2026-01-11T07:45:00+01:00", false)
	if err := e.RememberSwitchedOff(path); err == nil {
		t.Fatal("unreadable state file accepted silently")
	}
	if err := e.saveSwitchedOff(); err != nil {
		t.Fatal(err)
	}
	if got := savedSwitchOffs(t, path); got != `{ "switched_off": [] }` {
		t.Fatalf("saved %s", got)
	}
}

func TestEventLoopSavesSwitchOffsWhenItStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.state.json")
	t.Run("controller running", func(t *testing.T) {
		_, _, e := eventLoopFixture(t)
		if err := e.RememberSwitchedOff(path); err != nil {
			t.Fatal(err)
		}
		e.mu.Lock()
		e.lights["b"].switchedOff = true // as if the evening phase had just switched it off
		e.stateDirty = true
		e.mu.Unlock()
	}) // the fixture stops the loop when this subtest ends
	if got := savedSwitchOffs(t, path); got != `{ "switched_off": [ "b" ] }` {
		t.Fatalf("saved %s", got)
	}
}

func TestStatePathSitsBesideTheConfiguration(t *testing.T) {
	for config, want := range map[string]string{
		"config.json":                        "config.state.json",
		"/home/pi/hue/config.json":           "/home/pi/hue/config.state.json",
		"config/circadian-zurich.local.json": "config/circadian-zurich.local.state.json",
	} {
		if got := StatePath(config); got != want {
			t.Errorf("StatePath(%q) = %q, want %q", config, got, want)
		}
	}
}

func TestEventLoopSavesSwitchOffsEachSecond(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.state.json")
	_, ticks, e := eventLoopFixture(t)
	if err := e.RememberSwitchedOff(path); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.lights["k"].switchedOff = true // as if the evening phase had just switched it off
	e.stateDirty = true
	e.mu.Unlock()
	ticks <- time.Now()
	waitFor(t, func() bool { _, err := os.Stat(path); return err == nil })
	if got := savedSwitchOffs(t, path); got != `{ "switched_off": [ "k" ] }` {
		t.Fatalf("saved %s", got)
	}
}
