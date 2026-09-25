package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"onebyone/internal/processutil"
)

func findRG(configured string) (string, error) {
	if configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("configured rg is unavailable: %w", err)
		}
		return path, nil
	}
	name := "rg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if executable, err := os.Executable(); err == nil {
		// A launcher may invoke a symlink to the app/CLI. Resolve the actual
		// executable before looking for its bundled tools.
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
		dir := filepath.Dir(executable)
		for _, path := range []string{filepath.Join(dir, name), filepath.Join(dir, "bin", name), filepath.Join(dir, "tools", name), filepath.Join(dir, "..", "Resources", name)} {
			// LookPath checks executable permissions on Unix and executable
			// extensions on Windows; a regular file alone cannot be launched.
			if resolved, err := exec.LookPath(path); err == nil {
				return resolved, nil
			}
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", errors.New("同梱のファイル検索ツール（ripgrep）が見つからないか、実行できません。アプリを再インストールしてから再起動してください")
	}
	return path, nil
}

func (c *Catalog) validate(ctx context.Context, patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}
	args := []string{"--no-config", "--color", "never", "--text"}
	for _, p := range patterns {
		args = append(args, "-e", p)
	}
	args = append(args, "--", "-")
	_, err := c.run(ctx, "", strings.NewReader(""), args...)
	if err != nil {
		return fmt.Errorf("invalid regular expression: %w", err)
	}
	return nil
}

func (c *Catalog) matchFiles(ctx context.Context, root string, files, patterns []string) (map[string]bool, error) {
	matches := make(map[string]bool)
	if len(files) == 0 || len(patterns) == 0 {
		return matches, nil
	}
	base := []string{"--files-with-matches", "--null", "--text", "--no-config", "--color", "never", "--encoding", "none"}
	baseBytes := 0
	for _, pattern := range patterns {
		base = append(base, "-e", pattern)
		baseBytes += len(pattern) + 4
	}
	if baseBytes > 16000 {
		return nil, errors.New("combined regular expressions exceed 16000 bytes; split them into smaller rules")
	}
	base = append(base, "--")
	// Windows has a smaller process command-line limit than Unix. Bounded
	// chunks also make cancellation responsive with large source trees.
	for start := 0; start < len(files); {
		end, size := start, baseBytes
		for end < len(files) && end-start < 128 {
			cost := len(files[end])*2 + 4
			if end > start && size+cost > 24000 {
				break
			}
			size += cost
			end++
		}
		args := append(append([]string{}, base...), files[start:end]...)
		out, err := c.run(ctx, root, nil, args...)
		if err != nil {
			return nil, err
		}
		for _, file := range nulPaths(out) {
			matches[file] = true
		}
		start = end
	}
	return matches, nil
}

func (c *Catalog) run(ctx context.Context, dir string, input io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.rg, args...)
	processutil.HideWindow(cmd)
	cmd.Dir = dir
	cmd.Stdin = input
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return stdout.Bytes(), nil
		}
		message := strings.TrimSpace(stderr.String())
		if len(message) > 4000 {
			message = message[:4000]
		}
		return nil, fmt.Errorf("rg failed: %w: %s", err, message)
	}
	return stdout.Bytes(), nil
}

func nulPaths(output []byte) []string {
	var paths []string
	for _, item := range bytes.Split(output, []byte{0}) {
		if len(item) != 0 {
			paths = append(paths, filepath.ToSlash(string(item)))
		}
	}
	return paths
}
