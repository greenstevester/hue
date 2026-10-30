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
