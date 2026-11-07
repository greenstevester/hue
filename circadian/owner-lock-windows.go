//go:build windows

package circadian

import (
	"errors"
	"fmt"
	"syscall"
)

// errorSharingViolation is Windows error 32; the syscall package does not name it.
const errorSharingViolation = syscall.Errno(32)

// acquireOwnerLock opens the lock file with no sharing, so a second controller cannot open it.
// Windows closes the handle when the process dies, so a crash never leaves a stale owner behind.
func acquireOwnerLock(path string) (func(), error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, errorSharingViolation) {
		return nil, fmt.Errorf("another local circadian owner is running: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("open circadian lock %s: %w", path, err)
	}
	return func() { _ = syscall.CloseHandle(handle) }, nil
}
