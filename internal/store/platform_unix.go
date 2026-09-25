//go:build !windows

package store

import (
	"os"
	"syscall"
)

func replaceFile(from, to string) error          { return os.Rename(from, to) }
func openSnapshot(path string) (*os.File, error) { return os.Open(path) }
func lockFile(f *os.File) error                  { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
func unlockFile(f *os.File)                      { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
