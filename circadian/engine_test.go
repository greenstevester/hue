package circadian

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kungfusheep/hue/client"
)

type fakeBridge struct {
	mu     sync.Mutex
	writes []client.LightUpdate
	ids    []string
	err    error
	guard  func(context.Context, string, interface{}) error
	before func()
}

func (b *fakeBridge) UpdateLight(ctx context.Context, id string, u client.LightUpdate) error {
	if b.before != nil {
		b.before()
	}
	if b.guard != nil {
		if err := b.guard(ctx, "/resource/light/"+id, u); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writes = append(b.writes, u)
	b.ids = append(b.ids, id)
	return b.err
}

func engineFixture(t *testing.T) (*Engine, *fakeBridge, *time.Time) {
	t.Helper()
	c := DefaultConfig()
	c.Enabled = true
	now := instant(t, "2026-01-10T12:00:00+01:00")
	b := &fakeBridge{}
	l := client.Light{ID: "k", On: client.OnState{On: true}, HasDimming: true, Dimming: client.Dimming{Brightness: 50},
		ColorTemperature: &client.ColorTemperature{Mirek: 300, MirekValid: true,
			MirekSchema: &client.MirekSchema{MirekMinimum: 153, MirekMaximum: 454}}}
	e, err := NewEngine(c, b, []ManagedLight{{Light: l, Room: c.Rooms[0]}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	b.guard = e.Guard
	e.SetHealthy(true)
	e.Resume()
	return e, b, &now
}

func TestEngineSuppressesRepeatedTargetsAndHonoursManualExpiry(t *testing.T) {
	e, b, now := engineFixture(t)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(31 * time.Second)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 1 {
		t.Fatalf("repeated target: %d writes", len(b.writes))
	}
	e.Observe(client.EventData{ID: "k", Type: "light", Dimming: &client.Dimming{Brightness: 12}})
	if !e.Status().Lights[0].HoldUntil.Equal(now.Add(2 * time.Hour)) {
		t.Fatal("manual change did not start a hold")
	}
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 1 {
		t.Fatal("manual override reversed")
	}
	*now = now.Add(2 * time.Hour)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 2 {
		t.Fatal("override did not expire")
	}
}

func TestEngineAcceptsOwnEventsAndPreservesDeviceMetadata(t *testing.T) {
	e, b, now := engineFixture(t)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(11 * time.Second)
	u := b.writes[0]
	e.Observe(client.EventData{ID: "k", Type: "light", Dimming: u.Dimming, ColorTemperature: u.ColorTemperature})
	if !e.Status().Lights[0].HoldUntil.IsZero() {
		t.Fatal("own update treated as manual")
	}
	if e.lights["k"].managed.Light.ColorTemperature.MirekSchema == nil {
		t.Fatal("partial event discarded capability")
	}
}

func TestEngineManualOffRemainsOffAfterExpiry(t *testing.T) {
	e, b, now := engineFixture(t)
	e.Observe(client.EventData{ID: "k", Type: "light", On: &client.OnState{On: false}})
	*now = now.Add(3 * time.Hour)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 0 {
		t.Fatal("manual off was reversed")
	}
}

func TestEnginePausesDuringDisconnectionEntertainmentAndExplicitPause(t *testing.T) {
	for _, kind := range []string{"disconnected", "entertainment", "paused"} {
		t.Run(kind, func(t *testing.T) {
			e, b, _ := engineFixture(t)
			switch kind {
			case "disconnected":
				e.SetHealthy(false)
			case "entertainment":
				e.SetEntertainment([]client.Entertainment{{Status: "active",
					LightServices: []client.ResourceIdentifier{{RID: "k"}}}})
			case "paused":
				e.Pause()
			}
			if err := e.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(b.writes) != 0 {
				t.Fatal("suspended light changed")
			}
		})
	}
}

func TestManualWriteInvalidatesQueuedAutomation(t *testing.T) {
	e, b, _ := engineFixture(t)
	b.before = func() {
		if err := e.Guard(context.Background(), "/resource/light/k", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("obsolete automation permitted")
	}
	if len(b.writes) != 0 {
		t.Fatal("obsolete write reached Bridge")
	}
}

func TestEngineBridgeFailureRequiresRefreshBeforeRecovery(t *testing.T) {
	e, b, now := engineFixture(t)
	b.err = errors.New("disconnected")
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if e.Status().Healthy {
		t.Fatal("failed write left observation healthy")
	}
	*now = now.Add(31 * time.Second)
	b.err = nil
	e.SetHealthy(true)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 2 {
		t.Fatal("current target was not retried")
	}
}

func TestEngineConcurrentCommandsDoNotRace(t *testing.T) {
	e, b, _ := engineFixture(t)
	// A fixed injected clock keeps this test independent of shared mutable fake time.
	var group sync.WaitGroup
	for i := 0; i < 30; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			e.Pause()
			e.Resume()
			e.Status()
			e.Observe(client.EventData{ID: "k", Type: "light", Dimming: &client.Dimming{Brightness: 25}})
			_ = e.Guard(context.Background(), "/resource/scene/x", nil)
			_ = e.Tick(context.Background())
		}()
	}
	group.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range b.ids {
		if id != "k" {
			t.Fatal("unmanaged light changed")
		}
	}
}

func TestActivityOutlivesTimedHoldAndRestartsHoldOnCompletion(t *testing.T) {
	e, b, now := engineFixture(t)
	_ = e.Guard(context.Background(), "/activity", client.Activity{Targets: []string{"k"}, Active: true})
	*now = now.Add(3 * time.Hour)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 0 {
		t.Fatal("active effect lost ownership")
	}
	_ = e.Guard(context.Background(), "/activity", client.Activity{Targets: []string{"k"}, Active: false})
	if !e.Status().Lights[0].HoldUntil.Equal(now.Add(2 * time.Hour)) {
		t.Fatal("effect completion did not restart hold")
	}
}

func TestRoomMembershipChangeCannotBeResumedWithoutRediscovery(t *testing.T) {
	e, b, _ := engineFixture(t)
	if err := e.reconcileInventory(nil); err == nil {
		t.Fatal("missing kitchen light accepted")
	}
	e.Resume()
	if !e.Status().Paused {
		t.Fatal("stale room inventory resumed")
	}
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 0 {
		t.Fatal("stale inventory wrote lights")
	}
}

func TestOwnColourEventWithNullTemperatureDoesNotCreateHold(t *testing.T) {
	e, b, now := engineFixture(t)
	e.lights["k"].managed.Light.Color = &client.Color{XY: client.XY{X: .4, Y: .4}}
	*now = instant(t, "2026-01-10T23:00:00+01:00")
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(11 * time.Second)
	e.Observe(client.EventData{ID: "k", Type: "light", Color: b.writes[0].Color,
		Dimming: b.writes[0].Dimming, ColorTemperature: &client.ColorTemperature{Mirek: 0}})
	if !e.Status().Lights[0].HoldUntil.IsZero() {
		t.Fatal("own colour switch created a hold")
	}
}

func TestRateLimitDelayPreventsEarlyReplay(t *testing.T) {
	e, b, now := engineFixture(t)
	b.err = &client.ResponseError{Status: 429, RetryAfter: 2 * time.Minute}
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("expected rate limit")
	}
	*now = now.Add(time.Minute)
	e.SetHealthy(true)
	b.err = nil
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 1 {
		t.Fatal("Bridge retry delay ignored")
	}
	*now = now.Add(time.Minute)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 2 {
		t.Fatal("current target not retried after deadline")
	}
}

func TestOwnWhiteUpdateMayIncludeDerivedChromaticity(t *testing.T) {
	e, b, now := engineFixture(t)
	e.lights["k"].managed.Light.Color = &client.Color{XY: client.XY{X: .4, Y: .4}}
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(11 * time.Second)
	e.Observe(client.EventData{ID: "k", Type: "light", Dimming: b.writes[0].Dimming,
		ColorTemperature: b.writes[0].ColorTemperature, Color: &client.Color{XY: client.XY{X: .42, Y: .38}}})
	if !e.Status().Lights[0].HoldUntil.IsZero() {
		t.Fatal("derived white chromaticity created a hold")
	}
}

func TestImmediateBridgeTargetAndIntermediateFadeDoNotCreateManualHold(t *testing.T) {
	e, b, now := engineFixture(t)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Real Bridge events can announce the target before reporting intermediate values.
	for _, sample := range []struct {
		seconds    int
		brightness float64
	}{
		{0, b.writes[0].Dimming.Brightness}, {2, 55}, {4, 65}, {8, 78}, {12, 85},
	} {
		*now = instant(t, "2026-01-10T12:00:00+01:00").Add(time.Duration(sample.seconds) * time.Second)
		e.Observe(client.EventData{ID: "k", Type: "light", Dimming: &client.Dimming{Brightness: sample.brightness}})
		if !e.Status().Lights[0].HoldUntil.IsZero() {
			t.Fatalf("own fade at %ds started a manual hold", sample.seconds)
		}
	}
}

func TestTemperatureFadeFromColourModeUsesAdvertisedRange(t *testing.T) {
	e, b, now := engineFixture(t)
	e.lights["k"].managed.Light.ColorTemperature.Mirek = 0
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Second)
	e.Observe(client.EventData{ID: "k", Type: "light", ColorTemperature: &client.ColorTemperature{Mirek: 350}})
	if !e.Status().Lights[0].HoldUntil.IsZero() {
		t.Fatal("unknown starting white temperature caused a hold")
	}
	*now = now.Add(11 * time.Second)
	e.Observe(client.EventData{ID: "k", Type: "light", ColorTemperature: b.writes[0].ColorTemperature})
	if !e.Status().Lights[0].HoldUntil.IsZero() {
		t.Fatal("final white temperature caused a hold")
	}
}

func TestWhiteTargetReportedAsColourByThirdPartyBulbDoesNotCreateHold(t *testing.T) {
	e, b, now := engineFixture(t)
	s := e.lights["k"]
	s.managed.Light.Color = &client.Color{XY: client.XY{X: .3227, Y: .329}}
	s.managed.Light.ColorTemperature.Mirek = 0
	profile := e.config.Profiles["daytime"]
	profile.Brightness = 20
	e.config.Profiles["daytime"] = profile
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(31 * time.Second)
	snapshot := s.managed
	snapshot.Light.Dimming.Brightness = 20.16
	snapshot.Light.Color = &client.Color{XY: client.XY{X: .4081, Y: .3916}}
	snapshot.Light.ColorTemperature = &client.ColorTemperature{MirekSchema: s.managed.Light.ColorTemperature.MirekSchema}
	if err := e.reconcileInventory([]ManagedLight{snapshot}); err != nil {
		t.Fatal(err)
	}
	if !e.Status().Lights[0].HoldUntil.IsZero() {
		t.Fatal("third-party white chromaticity created a manual hold")
	}
	if b.writes[0].ColorTemperature == nil {
		t.Fatal("test must exercise a white-temperature command")
	}
}

func TestWebEnableDisablePreservesManualHoldsAndObservedState(t *testing.T) {
	e, b, now := engineFixture(t)
	e.Observe(client.EventData{ID: "k", Type: "light", Dimming: &client.Dimming{Brightness: 12}})
	hold := e.Status().Lights[0].HoldUntil
	if err := e.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if e.Status().Enabled {
		t.Fatal("automation not disabled")
	}
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 0 {
		t.Fatal("disabling wrote to a light")
	}
	if err := e.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	state := e.Status()
	if !state.Enabled || !state.Lights[0].HoldUntil.Equal(hold) || state.Lights[0].Brightness != 12 {
		t.Fatal("enabling lost observed state or manual priority")
	}
	*now = now.Add(2 * time.Hour)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 1 {
		t.Fatal("enabled schedule did not resume after manual hold")
	}
	e.invalidateInventory()
	if err := e.SetEnabled(true); err == nil {
		t.Fatal("invalid inventory was enabled")
	}
}

func TestPerLightDisableStopsWritesAndRemainsVisible(t *testing.T) {
	e, b, now := engineFixture(t)
	if err := e.SetLightEnabled("k", false); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := e.Status()
	if len(b.writes) != 0 || len(state.Lights) != 1 || state.Lights[0].Enabled || !state.Lights[0].On {
		t.Fatal("excluded light changed state or disappeared")
	}
	if err := e.SetLightEnabled("office", true); err == nil {
		t.Fatal("unmanaged light accepted")
	}
	if err := e.SetLightEnabled("k", true); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 1 {
		t.Fatal("reenabled light did not follow schedule")
	}
}

func TestTimingOverridesApplyWithoutClearingManualHold(t *testing.T) {
	e, _, now := engineFixture(t)
	*now = instant(t, "2026-01-10T07:40:00+01:00")
	e.Observe(client.EventData{ID: "k", Type: "light", Dimming: &client.Dimming{Brightness: 12}})
	hold := e.Status().Lights[0].HoldUntil
	if err := e.SetSchedule("09:00", "23:00"); err != nil {
		t.Fatal(err)
	}
	state := e.Status()
	if state.Lights[0].Target.Phase != "night" || !state.Lights[0].HoldUntil.Equal(hold) {
		t.Fatal("schedule override did not preserve manual priority")
	}
	if err := e.SetSchedule("invalid", "23:00"); err == nil {
		t.Fatal("invalid time accepted")
	}
}

func TestResumeDoesNotRestartScheduleSwitchedOffInConfiguration(t *testing.T) {
	c := DefaultConfig()
	c.Enabled = false // the web switch saved "off"; run-web.sh restarts with --resume
	b := &fakeBridge{}
	l := client.Light{ID: "k", On: client.OnState{On: true}, HasDimming: true, Dimming: client.Dimming{Brightness: 50}}
	e, err := NewEngine(c, b, []ManagedLight{{Light: l, Room: c.Rooms[0]}}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	b.guard = e.Guard
	e.SetHealthy(true)
	e.Resume()
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 0 || e.Status().Enabled {
		t.Fatalf("schedule switched off in configuration came back after resume: %d writes", len(b.writes))
	}
}

func TestLightReturnsToScheduleAfterEffectAndHoldEnd(t *testing.T) {
	e, b, now := engineFixture(t)
	_ = e.Guard(context.Background(), "/activity", client.Activity{Targets: []string{"k"}, Active: true})
	_ = e.Guard(context.Background(), "/activity", client.Activity{Targets: []string{"k"}, Active: false})
	*now = now.Add(2*time.Hour + time.Second)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 1 {
		t.Fatal("light stayed off the schedule after its effect and hold ended")
	}
}

func TestSwitchingOnFromOutsideTakesTheScheduleAtOnce(t *testing.T) {
	// A light comes back on showing whatever it had when it went off, such as last night's deep red.
	e, b := whiteBulbEngine(t, "2026-01-10T12:00:00+01:00", false)
	start(t, e)
	// The Bridge reports a plain switch-on as {"on":{"on":true}} alone (seen live on 9 November).
	// An unchanged brightness alongside it must not count as a choice either.
	e.Observe(client.EventData{ID: "k", Type: "light", On: &client.OnState{On: true},
		Dimming: &client.Dimming{Brightness: 50}})
	if !e.Status().Lights[0].HoldUntil.IsZero() {
		t.Fatal("switching a light on held its old colour for two hours")
	}
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 1 {
		t.Fatalf("switched-on light did not get the daytime target: %d writes", len(b.writes))
	}
	w := b.writes[0]
	if w.ColorTemperature == nil || w.ColorTemperature.Mirek != 286 || w.Dimming == nil || w.Dimming.Brightness != 85 {
		t.Fatalf("switched-on light got %+v, want daytime 3500 K at 85%%", w)
	}
	if w.Dynamics == nil || w.Dynamics.Duration != 1000 {
		t.Fatalf("switched-on light should reach the target within a second, got %+v", w.Dynamics)
	}
}

func TestSwitchingOnEndsTheHoldLeftBySwitchingOff(t *testing.T) {
	e, b, now := engineFixture(t)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Observe(client.EventData{ID: "k", Type: "light", On: &client.OnState{On: false}}) // someone switches it off
	*now = now.Add(30 * time.Minute)
	e.Observe(client.EventData{ID: "k", Type: "light", On: &client.OnState{On: true}}) // and on again
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.writes) != 2 {
		t.Fatalf("light switched off and on again stayed on hold: %d writes", len(b.writes))
	}
}

func TestSwitchingOnWithAChosenLookStillHolds(t *testing.T) {
	e, b := whiteBulbEngine(t, "2026-01-10T12:00:00+01:00", false)
	start(t, e)
	// A scene or a dimmer switch turns the light on at a brightness of its own.
	e.Observe(client.EventData{ID: "k", Type: "light", On: &client.OnState{On: true},
		Dimming: &client.Dimming{Brightness: 20}})
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.Status().Lights[0].HoldUntil.IsZero() || len(b.writes) != 0 {
		t.Fatalf("light switched on with its own brightness was changed: %d writes", len(b.writes))
	}
}

func TestWhiteBulbSwitchedOnDuringAColourPhaseStaysOn(t *testing.T) {
	// The evening phase is colour only, which a white bulb cannot show, so the schedule would switch it off.
	e, b := whiteBulbEngine(t, "2026-01-10T20:00:00+01:00", false)
	start(t, e)
	e.Observe(client.EventData{ID: "k", Type: "light", On: &client.OnState{On: true}})
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.Status().Lights[0].HoldUntil.IsZero() || len(b.writes) != 0 {
		t.Fatalf("white bulb switched on in the evening was switched off again: %+v", b.writes)
	}
}
