package circadian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kungfusheep/hue/client"
)

// Start discovers the two rooms, acquires local ownership, and starts paused.
// The returned stop function cancels all work and waits for cleanup.
func Start(
	parent context.Context, c *client.Client, config Config, logf func(string, ...interface{}),
) (*Engine, <-chan error, func(), error) {
	if err := config.Validate(); err != nil {
		return nil, nil, nil, err
	}
	if !config.Enabled {
		return nil, nil, nil, fmt.Errorf("circadian control is disabled")
	}
	lockPath := filepath.Join(os.TempDir(), fmt.Sprintf("hue-circadian-%x.lock", c.BridgeAddress()))
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, nil, nil, fmt.Errorf("another local circadian owner is running: %w", err)
	}
	release := func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }
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
	managed, err := SelectLights(config, rooms, devices, lights)
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
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); defer close(done); defer release(); done <- Run(ctx, c, engine, logf) }()
	stop := func() { cancel(); <-finished }
	return engine, done, stop, nil
}
