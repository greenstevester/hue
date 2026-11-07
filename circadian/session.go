package circadian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kungfusheep/hue/client"
)

// Start discovers the two rooms, acquires local ownership, and starts paused.
// statePath names the file that remembers the schedule's switch-offs (see StatePath); empty keeps
// them in memory only. The returned stop function cancels all work and waits for cleanup.
func Start(
	parent context.Context, c *client.Client, config Config, statePath string,
	logf func(string, ...interface{}),
) (*Engine, <-chan error, func(), error) {
	if err := config.Validate(); err != nil {
		return nil, nil, nil, err
	}
	lockPath := filepath.Join(os.TempDir(), fmt.Sprintf("hue-circadian-%x.lock", c.BridgeAddress()))
	release, err := acquireOwnerLock(lockPath)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	requestCtx, requestCancel := context.WithTimeout(ctx, 30*time.Second)
	rooms, err := c.GetRooms(requestCtx)
	if err != nil {
		requestCancel()
		cancel()
		release()
		return nil, nil, nil, err
	}
	devices, err := c.GetDevices(requestCtx)
	if err != nil {
		requestCancel()
		cancel()
		release()
		return nil, nil, nil, err
	}
	lights, err := c.GetLights(requestCtx)
	requestCancel()
	if err != nil {
		cancel()
		release()
		return nil, nil, nil, err
	}
	managed, err := RoomLights(config, rooms, devices, lights)
	if err != nil {
		cancel()
		release()
		return nil, nil, nil, err
	}
	engine, err := NewEngine(config, c, managed, time.Now)
	if err != nil {
		cancel()
		release()
		return nil, nil, nil, err
	}
	for _, l := range managed {
		logf("circadian selected %s: %s (%s)", l.Room.Name, l.Light.Metadata.Name, l.Light.ID)
	}
	if config.Latitude == nil {
		logf("circadian uses fixed sunrise/sunset times; location is not configured")
	}
	// Restore before the event loop starts, so no refresh or resume can run ahead of it.
	if statePath != "" {
		if err := engine.RememberSwitchedOff(statePath); err != nil {
			logf("circadian state: %v", err)
		}
	}
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); defer close(done); defer release(); done <- Run(ctx, c, engine, logf) }()
	stop := func() { cancel(); <-finished }
	return engine, done, stop, nil
}
