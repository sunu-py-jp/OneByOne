//go:build windows

package store

import (
	"errors"
	"syscall"
)

func workspaceLockBusy(err error) bool {
	// ERROR_LOCK_VIOLATION. Access-denied and filesystem errors must not be
	// mistaken for another editor, nor permit edits without an acquired lease.
	return errors.Is(err, syscall.Errno(33))
}

func workspaceNonblockFlag() int { return 0 }
