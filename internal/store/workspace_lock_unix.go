//go:build !windows

package store

import (
	"errors"
	"syscall"
)

func workspaceLockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func workspaceNonblockFlag() int { return syscall.O_NONBLOCK }
