package rulepack

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var ruleID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func portablePath(name string) error {
	if name == "" || len(name) > 4096 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:*?\"<>|") || strings.HasPrefix(name, "/") || len(strings.Split(name, "/")) > 32 {
		return fmt.Errorf("安全でないパッケージ内パスです: %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || len(part) > 255 {
			return fmt.Errorf("安全でないパッケージ内パスです: %q", name)
		}
		for _, r := range part {
			if unicode.IsControl(r) {
				return fmt.Errorf("制御文字を含むパッケージ内パスです")
			}
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CONIN$" || stem == "CONOUT$" || ((strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && strings.Contains("123456789¹²³", strings.TrimPrefix(strings.TrimPrefix(stem, "COM"), "LPT")) && len([]rune(stem)) == 4) {
			return fmt.Errorf("Windowsで使用できないパスです: %q", name)
		}
	}
	return nil
}

func allowedPath(name string, directory bool) error {
	if err := portablePath(name); err != nil {
		return err
	}
	parts := strings.Split(name, "/")
	if directory {
		if name == "rules" || name == "patterns" {
			return nil
		}
		if len(parts) >= 2 && parts[0] == "rules" && ruleID.MatchString(parts[1]) {
			return nil
		}
	} else {
		if name == "package.json" || name == "patterns/legacy-symbols.txt" {
			return nil
		}
		if len(parts) >= 3 && parts[0] == "rules" && ruleID.MatchString(parts[1]) {
			if len(parts) == 3 {
				switch strings.ToLower(parts[2]) {
				case "rule.md", "pattern.txt", "name.txt":
					return fmt.Errorf("旧形式のルール定義は使用できません。rule.jsonを指定してください: %q", name)
				case "rule.json":
					if parts[2] != "rule.json" {
						return fmt.Errorf("定義ファイル名はrule.jsonにしてください: %q", name)
					}
				}
			}
			base := strings.ToLower(path.Base(name))
			switch base {
			case "credentials.json", "llm-settings.json", "azure-settings.json", "master-key.json":
				return fmt.Errorf("認証設定はルールパッケージに含められません")
			}
			switch strings.ToLower(path.Ext(base)) {
			case ".pem", ".key", ".p12", ".pfx":
				return fmt.Errorf("認証ファイルはルールパッケージに含められません")
			}
			return nil
		}
	}
	return fmt.Errorf("ルールパッケージの対象外のパスです: %q", name)
}

type pathNode struct {
	name      string
	directory bool
}
type pathIndex map[string]pathNode

func (index pathIndex) add(name string, directory bool) error {
	parts := strings.Split(name, "/")
	for i := 1; i <= len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		dir := i < len(parts) || directory
		key := cases.Fold().String(norm.NFC.String(prefix))
		if old, ok := index[key]; ok {
			if old.name != prefix || old.directory != dir || (!dir && i == len(parts)) {
				return fmt.Errorf("大文字小文字・Unicode表記またはファイル配置が衝突しています: %q", name)
			}
		} else {
			index[key] = pathNode{prefix, dir}
		}
	}
	return nil
}

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
