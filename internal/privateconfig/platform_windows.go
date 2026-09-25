//go:build windows

package privateconfig

import (
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const keyProtection = "windows-user-dpapi"

// Root confines traversal, and openRegular checks the opened file identity and
// rejects reparse links before reading or mutating their contents.
const noFollowFlag = 0

func protectKey(key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, ErrInvalidStore
	}
	input := windows.DataBlob{Size: uint32(len(key)), Data: &key[0]}
	var output windows.DataBlob
	err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	runtime.KeepAlive(key)
	if err != nil {
		return nil, ErrInvalidStore
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	if output.Data == nil || output.Size == 0 || output.Size > maxKeyBytes/2 {
		return nil, ErrInvalidStore
	}
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}

func unprotectKey(protected []byte) ([]byte, error) {
	if len(protected) == 0 || len(protected) > maxKeyBytes/2 {
		return nil, ErrInvalidStore
	}
	input := windows.DataBlob{Size: uint32(len(protected)), Data: &protected[0]}
	var output windows.DataBlob
	err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	runtime.KeepAlive(protected)
	if err != nil {
		return nil, ErrInvalidStore
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	if output.Data == nil || output.Size != 32 {
		return nil, ErrInvalidStore
	}
	plaintext := unsafe.Slice(output.Data, int(output.Size))
	defer clear(plaintext)
	return append([]byte(nil), plaintext...), nil
}

func lockPrivateFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{})
}

func unlockPrivateFile(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}
