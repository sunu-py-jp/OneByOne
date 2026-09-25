package engine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"onebyone/internal/model"
)

// New output directories follow the source folder's permission bits. Existing
// directories are never chmodded: their owner may have chosen a private output
// location. Native ACLs are inherited from the output parent, not copied from a
// different source directory, so sharing requires a suitable parent ACL/group.
func sharedOutputPolicy(cfg model.Config) (os.FileMode, os.FileMode, error) {
	if strings.TrimSpace(cfg.Root) == "" {
		return 0, 0, fmt.Errorf("対象フォルダが未設定です")
	}
	root, err := settingsRoot(cfg.Root)
	if err != nil {
		return 0, 0, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return 0, 0, err
	}
	directoryMode := info.Mode().Perm() | 0700
	return directoryMode, directoryMode & 0666, nil
}

func outputAbsolute(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("キュー保存先が未設定です")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return canonicalAlias(abs), nil
}

// ensureSharedOutputDirectory only grants the selected policy to newly-created
// directories. A pre-existing file, symlink, or incompatible access policy is
// not replaced or silently broadened.
func ensureSharedOutputDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("出力先に実体のあるフォルダを指定してください: %s", path)
		}
		if parent := filepath.Dir(path); parent != path {
			return ensureSharedOutputDirectory(parent, mode)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return err
	}
	if err = ensureSharedOutputDirectory(parent, mode); err != nil {
		return err
	}
	if err = os.Mkdir(path, mode); os.IsExist(err) {
		// Another creator won. Its existing policy must remain unchanged.
		return ensureSharedOutputDirectory(path, mode)
	}
	if err != nil {
		return err
	}
	// Override umask only on the directory this call created, so a deliberately
	// group-writable source does not produce owner-only output descendants.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return fmt.Errorf("出力先フォルダが変更されました: %s", path)
	}
	return f.Chmod(mode)
}

func prepareSharedOutput(cfg model.Config) error {
	mode, _, err := sharedOutputPolicy(cfg)
	if err != nil {
		return err
	}
	queue, err := outputAbsolute(cfg.QueuePath)
	if err != nil {
		return err
	}
	for _, path := range []string{filepath.Dir(queue), queue + ".artifacts", filepath.Join(filepath.Dir(queue), "reports")} {
		if err = ensureSharedOutputDirectory(path, mode); err != nil {
			return err
		}
	}
	return nil
}

// writeSharedArtifact also writes shared queue/manifest/report snapshots. A
// source path supplied by the runner caps the artifact's permissions to those
// of that individual source file; a private file must not become public merely
// because its containing source directory is readable by other accounts.
func writeSharedArtifact(cfg model.Config, path string, data []byte, sourcePaths ...string) error {
	dirMode, fileMode, err := sharedOutputPolicy(cfg)
	if err != nil {
		return err
	}
	path, err = outputAbsolute(path)
	if err != nil {
		return err
	}
	if err = ensureSharedOutputDirectory(filepath.Dir(path), dirMode); err != nil {
		return err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(path)
	checkDestination := func() (os.FileInfo, error) {
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("出力先が通常のファイルではありません: %s", path)
		}
		return info, nil
	}
	existing, err := checkDestination()
	if err != nil {
		return err
	}
	if existing != nil {
		fileMode = existing.Mode().Perm()
	}
	for _, sourcePath := range sourcePaths {
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("差分の元ファイルが通常のファイルではありません: %s", sourcePath)
		}
		allowed := info.Mode().Perm()&0666 | 0600
		if existing != nil && fileMode&0077&^allowed != 0 {
			return fmt.Errorf("既存の差分ファイルは元ファイルより公開範囲が広いため上書きできません: %s", path)
		}
		if existing == nil {
			fileMode &= allowed
		}
	}
	var suffix [16]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		return err
	}
	temp := ".onebyone-output-" + hex.EncodeToString(suffix[:])
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(fileMode)
	}
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
	current, err := checkDestination()
	if err != nil {
		return err
	}
	if (existing == nil) != (current == nil) || existing != nil && (!os.SameFile(existing, current) || existing.Mode().Perm() != current.Mode().Perm()) {
		return fmt.Errorf("出力先ファイルが保存中に変更されました: %s", path)
	}
	if err = root.Rename(temp, name); err != nil {
		return err
	}
	if directory, err := root.Open("."); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
