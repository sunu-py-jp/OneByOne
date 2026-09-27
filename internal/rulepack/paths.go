package rulepack

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Reject application-controlled symlinks in ancestors, while allowing only the
// standard macOS /tmp and /var aliases used by the OS itself.
func checkedAbsolute(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("パスを指定してください")
	}
	full, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	for p := full; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			alias := false
			if runtime.GOOS == "darwin" && (p == "/tmp" || p == "/var") {
				target, e := os.Readlink(p)
				alias = e == nil && (target == "private"+p || target == "/private"+p)
			}
			if !alias {
				return "", fmt.Errorf("シンボリックリンクは利用できません: %s", p)
			}
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return full, nil
}

func openDirectory(name string) (*os.Root, error) {
	full, err := checkedAbsolute(name)
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(full)
	if err != nil || !before.IsDir() {
		return nil, fmt.Errorf("フォルダを開けません")
	}
	root, err := os.OpenRoot(full)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, fmt.Errorf("フォルダが読み込み中に変更されました")
	}
	return root, nil
}

func openRegular(root *os.Root, name string) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("通常ファイル以外は利用できません: %s", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	after, afterErr := root.Lstat(name)
	if err != nil || afterErr != nil || !opened.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		f.Close()
		return nil, fmt.Errorf("ファイルが読み込み中に変更されました: %s", name)
	}
	return f, nil
}
