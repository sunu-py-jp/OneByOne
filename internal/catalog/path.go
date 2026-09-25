package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PathWithin resolves an existing repository path without following symlinks.
// Both separator styles are checked, so persisted queues are portable and cannot
// acquire a different meaning when copied between operating systems.
func PathWithin(root, relative string) (string, error) {
	if relative == "" || len(relative) > 4096 || strings.ContainsAny(relative, ":*?\"<>|\x00\r\n") {
		return "", fmt.Errorf("unsafe relative path: %q", relative)
	}
	portable := strings.ReplaceAll(relative, "\\", "/")
	if strings.HasPrefix(portable, "/") || filepath.IsAbs(relative) {
		return "", fmt.Errorf("absolute path is not allowed: %q", relative)
	}
	for _, part := range strings.Split(portable, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return "", fmt.Errorf("ambiguous or traversing path is not allowed: %q", relative)
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
			(len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
			return "", fmt.Errorf("reserved Windows device path is not allowed: %q", relative)
		}
	}
	absRoot, err := checkedAbsolute(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absRoot)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("source root is not a directory: %s", root)
	}
	full := filepath.Join(absRoot, filepath.FromSlash(portable))
	rel, err := filepath.Rel(absRoot, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes source root: %q", relative)
	}
	if err := rejectSymlinks(full); err != nil {
		return "", err
	}
	return full, nil
}

func checkedAbsolute(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("path is empty or contains NUL")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := rejectSymlinks(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func rejectSymlinks(full string) error {
	// Check ancestors too: inspecting only the final file would miss a symlinked
	// parent directory and allow reads or writes outside the selected workspace.
	for current := full; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// macOS exposes its standard temporary directories through these
			// system aliases. They are not project-controlled symlinks.
			systemAlias := false
			if runtime.GOOS == "darwin" && (current == "/var" || current == "/tmp") {
				target, readErr := os.Readlink(current)
				systemAlias = readErr == nil && (target == "private"+current || target == "/private"+current)
			}
			if !systemAlias {
				return fmt.Errorf("symlinks are not allowed: %s", current)
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return nil
}
