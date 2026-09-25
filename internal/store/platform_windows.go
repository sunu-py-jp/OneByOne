//go:build windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

var kernel = syscall.NewLazyDLL("kernel32.dll")

const snapshotRetryTimeout = 2 * time.Second

// A reader without FILE_SHARE_DELETE, antivirus, or an indexer can briefly
// prevent replacement on Windows. Retry only sharing/access conflicts and
// keep the old destination intact if the conflict does not clear. In
// particular, never fall back to removing or truncating the destination.
func retrySnapshotOperation(operation func() error) error {
	deadline := time.Now().Add(snapshotRetryTimeout)
	delay := 5 * time.Millisecond
	for {
		err := operation()
		if err == nil || !(errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))) {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		time.Sleep(min(delay, remaining))
		delay = min(delay*2, 50*time.Millisecond)
	}
}

// Snapshot readers allow deletion/renaming while retaining the complete old
// snapshot on their existing handle. Ordinary os.Open omits FILE_SHARE_DELETE.
func openSnapshot(path string) (*os.File, error) {
	if path == "" {
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.ERROR_FILE_NOT_FOUND}
	}
	var file *os.File
	err := retrySnapshotOperation(func() error {
		root, openErr := os.OpenRoot(filepath.Dir(path))
		if openErr != nil {
			return openErr
		}
		defer root.Close()
		// Unlike os.Open, Root.Open shares delete access on Windows. It also
		// retains Go's long-path handling and confines app snapshots to their
		// parent directory instead of following a replaced symlink outside it.
		file, openErr = root.Open(filepath.Base(path))
		return openErr
	})
	if err != nil {
		return nil, err
	}
	return file, nil
}

func replaceFile(from, to string) error {
	// WriteFile always creates its temporary file beside the destination.
	// Enforce that invariant before reducing either path to a basename.
	if filepath.Clean(filepath.Dir(from)) != filepath.Clean(filepath.Dir(to)) {
		return &os.LinkError{Op: "replace", Old: from, New: to, Err: syscall.EXDEV}
	}
	root, err := os.OpenRoot(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer root.Close()
	// Root.Rename uses Windows POSIX rename semantics (Go 1.25+), which
	// preserve open readers' old snapshots. MoveFileEx cannot replace even
	// a FILE_SHARE_DELETE reader. The OS operation stays atomic; do not use
	// ReplaceFileW, whose documented partial failures can remove the old file.
	return retrySnapshotOperation(func() error {
		return root.Rename(filepath.Base(from), filepath.Base(to))
	})
}

func lockFile(f *os.File) error {
	var ov syscall.Overlapped
	r, _, err := kernel.NewProc("LockFileEx").Call(f.Fd(), 3, 0, 0xffffffff, 0, uintptr(unsafe.Pointer(&ov)))
	if r == 0 {
		return err
	}
	return nil
}
func unlockFile(f *os.File) {
	var ov syscall.Overlapped
	kernel.NewProc("UnlockFileEx").Call(f.Fd(), 0, 0xffffffff, 0, uintptr(unsafe.Pointer(&ov)))
}
