package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// candidateSnapshotDiff compares two immutable byte snapshots in a private,
// candidate-specific temporary directory. Neither Git's index nor a worktree
// participates in the comparison. In particular, exit code 1 means a diff was
// found, while truncated output and other failures must never be called valid.
func candidateSnapshotDiff(ctx context.Context, parent, relative string, before, after []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, ".onebyone-candidate-diff-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	for name, content := range map[string][]byte{"before": before, "after": after} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			return "", err
		}
	}
	cmd, err := runtimeCommand(ctx, "git", "--no-pager", "-c", "core.quotePath=false", "-c", "core.autocrlf=false", "-c", "core.fsmonitor=false", "diff", "--no-index", "--no-prefix", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--text", "--diff-algorithm=myers", "--unified=3", "--", "before", "after")
	if err != nil {
		return "", err
	}
	cmd.Dir = dir
	// Ignore any repository above the artifact directory, including its local
	// attributes/configuration. Global/system Git settings are disabled by the
	// same bundled-runtime wrapper used by all other engine Git operations.
	cmd.Env = append(cmd.Env, "GIT_CEILING_DIRECTORIES="+dir)
	cmd.WaitDelay = 3 * time.Second
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return "", fmt.Errorf("候補の差分を作成できません: %w\n%s", err, stderr.String())
	}
	if err == nil {
		return "", nil
	}
	// The temporary names are implementation details; artifacts use the same
	// paths as a regular Git diff of the target file, without touching that file.
	a, b := candidateDiffPath("a/"+relative), candidateDiffPath("b/"+relative)
	lines := strings.SplitAfter(out.String(), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "@@ ") {
			break
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			lines[i] = "diff --git " + a + " " + b + "\n"
		case strings.HasPrefix(line, "--- "):
			lines[i] = "--- " + a + "\n"
		case strings.HasPrefix(line, "+++ "):
			lines[i] = "+++ " + b + "\n"
		}
	}
	return strings.Join(lines, ""), nil
}

func candidateDiffPath(path string) string {
	if strings.ContainsAny(path, " \t\r\n\\\"") {
		return strconv.Quote(path)
	}
	return path
}
