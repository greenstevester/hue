//go:build !windows

package circadian

import (
	"fmt"
	"os"
	"syscall"
)

// acquireOwnerLock lets one controller per Bridge run on this machine. The kernel drops the lock if
// the process dies, so a crash never leaves a stale owner behind.
func acquireOwnerLock(path string) (func(), error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("another local circadian owner is running: %w", err)
	}
	return func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }, nil
}
