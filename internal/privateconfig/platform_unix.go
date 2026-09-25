//go:build !windows

package privateconfig

import (
	"errors"
	"os"
	"syscall"
)

const keyProtection = "owner-only"
const noFollowFlag = syscall.O_NOFOLLOW

// On macOS/Linux the key is protected by the private directory and file modes.
// This does not protect against another process already running as this user.
func protectKey(key []byte) ([]byte, error)   { return append([]byte(nil), key...), nil }
func unprotectKey(key []byte) ([]byte, error) { return append([]byte(nil), key...), nil }

func lockPrivateFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func unlockPrivateFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
