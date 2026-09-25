package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const workspaceOwnerLimit = 16 << 10

// WorkspaceOwner is display metadata, not proof of ownership. Only the live OS
// lock determines whether a workspace is in use.
type WorkspaceOwner struct {
	Owner    string `json:"owner"`
	Host     string `json:"host"`
	OpenedAt string `json:"openedAt"`
}

// WorkspaceLease keeps the same lock-file inode open until Release or process
// exit. Never remove or replace the lock file, including when releasing it.
type WorkspaceLease struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *WorkspaceLease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		unlockFile(l.file)
		l.err = l.file.Close()
	})
	return l.err
}

// AcquireWorkspace acquires an editing lease. A busy workspace returns a nil
// lease and a non-nil owner (possibly empty while metadata is unavailable).
// Errors, including permission errors, must not be treated as an available
// workspace. The caller must validate that the parent is its workspace folder.
//
// This coordinates processes using the same filesystem lock service, including
// a shared filesystem that supports these OS locks. Independently synchronized
// local copies (for example, cloud sync folders) do not share OS locks.
func AcquireWorkspace(path string, owner WorkspaceOwner) (*WorkspaceLease, *WorkspaceOwner, error) {
	root, name, err := workspaceLockRoot(path)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	f, err := openWorkspaceLock(root, name, true)
	if err != nil {
		return nil, nil, err
	}
	if err = lockFile(f); err != nil {
		_ = f.Close()
		if !workspaceLockBusy(err) {
			return nil, nil, fmt.Errorf("ワークスペースの編集中確認に失敗しました: %w", err)
		}
		other, err := readWorkspaceOwner(root, name+".owner.json")
		return nil, other, err
	}
	lease := &WorkspaceLease{file: f}
	if err = verifyWorkspaceFile(root, name, f); err != nil {
		_ = lease.Release()
		return nil, nil, err
	}
	if owner.OpenedAt == "" {
		owner.OpenedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err = writeWorkspaceOwner(root, name+".owner.json", owner); err != nil {
		_ = lease.Release()
		return nil, nil, err
	}
	return lease, nil, nil
}

// PeekWorkspace checks an existing lock without creating, truncating, or
// updating either file. A stale owner file is ignored when no live lock exists.
// An exclusive OS lock is attempted on a read-only handle and released at once.
func PeekWorkspace(path string) (*WorkspaceOwner, error) {
	root, name, err := workspaceLockRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = workspaceRegularFile(root, name+".owner.json", true); err != nil {
		return nil, err
	}
	f, err := openWorkspaceLock(root, name, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err = lockFile(f); err != nil {
		if workspaceLockBusy(err) {
			return readWorkspaceOwner(root, name+".owner.json")
		}
		return nil, fmt.Errorf("ワークスペースの編集中確認に失敗しました: %w", err)
	}
	defer unlockFile(f)
	if err = verifyWorkspaceFile(root, name, f); err != nil {
		return nil, err
	}
	return nil, nil
}

func workspaceLockRoot(path string) (*os.Root, string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return nil, "", fmt.Errorf("ワークスペースのロックパスが不正です")
	}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return nil, "", fmt.Errorf("ワークスペースのロックパスに親ディレクトリ参照は使用できません")
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	parent, name := filepath.Dir(path), filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return nil, "", fmt.Errorf("ワークスペースのロックファイル名が不正です")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("ワークスペースのロック保存先は実体のあるフォルダを指定してください")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, "", err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		if err != nil {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("ワークスペースのロック保存先が変更されました")
	}
	return root, name, nil
}

func workspaceRegularFile(root *os.Root, name string, allowMissing bool) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("ワークスペースのロック関連ファイルが通常のファイルではありません: %s", name)
	}
	return nil
}

func verifyWorkspaceFile(root *os.Root, name string, f *os.File) error {
	if err := workspaceRegularFile(root, name, false); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return fmt.Errorf("ワークスペースのロック関連ファイルが変更されました: %s", name)
	}
	return nil
}

func openWorkspaceLock(root *os.Root, name string, create bool) (*os.File, error) {
	if err := workspaceRegularFile(root, name, create); err != nil {
		return nil, err
	}
	// Existing files need only read access. This allows another account with
	// read access to inspect the lock even if the creator's umask removed write.
	f, err := root.OpenFile(name, os.O_RDONLY|workspaceNonblockFlag(), 0)
	if create && errors.Is(err, os.ErrNotExist) {
		f, err = root.OpenFile(name, os.O_RDONLY|os.O_CREATE|os.O_EXCL|workspaceNonblockFlag(), 0666)
		if errors.Is(err, os.ErrExist) {
			f, err = root.OpenFile(name, os.O_RDONLY|workspaceNonblockFlag(), 0)
		}
	}
	if err != nil {
		return nil, err
	}
	if err = verifyWorkspaceFile(root, name, f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func readWorkspaceOwner(root *os.Root, name string) (*WorkspaceOwner, error) {
	if err := workspaceRegularFile(root, name, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &WorkspaceOwner{}, nil
		}
		return nil, err
	}
	f, err := root.OpenFile(name, os.O_RDONLY|workspaceNonblockFlag(), 0)
	if errors.Is(err, os.ErrNotExist) {
		return &WorkspaceOwner{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err = verifyWorkspaceFile(root, name, f); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, workspaceOwnerLimit+1))
	if err != nil {
		return nil, err
	}
	var owner WorkspaceOwner
	if len(data) > workspaceOwnerLimit || json.Unmarshal(data, &owner) != nil {
		// Busy status comes from the OS lock and remains true even if its
		// diagnostic display metadata is missing, truncated, or oversized.
		return &WorkspaceOwner{}, nil
	}
	return &owner, nil
}

func writeWorkspaceOwner(root *os.Root, name string, owner WorkspaceOwner) error {
	if err := workspaceRegularFile(root, name, true); err != nil {
		return err
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	if len(data) > workspaceOwnerLimit {
		return fmt.Errorf("ワークスペースの編集者情報が長すぎます")
	}
	var suffix [16]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		return err
	}
	temp := ".workspace-owner-" + hex.EncodeToString(suffix[:])
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = workspaceRegularFile(root, name, true); err != nil {
		return err
	}
	return root.Rename(temp, name)
}
