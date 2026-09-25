package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"onebyone/internal/model"
)

const gitInstallationTimeout = 4 * time.Second
const gitInstallationOutputLimit = 4096

var gitVersionLine = regexp.MustCompile(`^git version [0-9]+\.[0-9]+[a-zA-Z0-9.+() -]*$`)

// CheckGitInstallation uses the same bundled Git as the runner. Version probing is
// bounded and does not invoke a shell, an installer, or a repository command.
func CheckGitInstallation(ctx context.Context) model.GitInstallation {
	rt, err := resolveGitRuntime()
	if rt.Root != "" && err != nil {
		return model.GitInstallation{Platform: runtime.GOOS, Status: "bundle_error", Path: rt.Path, Message: errBundledGit.Error()}
	}
	result := checkGitInstallation(ctx, runtime.GOOS, func(string) (string, error) { return rt.Path, err }, gitInstallationCommand)
	if rt.Root != "" && !result.Available {
		result.Status, result.Message = "bundle_error", errBundledGit.Error()
	}
	return result
}

func checkGitInstallation(ctx context.Context, platform string, lookPath func(string) (string, error), run func(context.Context, string, ...string) (string, error)) model.GitInstallation {
	ctx, cancel := context.WithTimeout(ctx, gitInstallationTimeout)
	defer cancel()
	result := model.GitInstallation{Platform: platform, Status: "unusable"}
	path, err := lookPath("git")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || os.IsNotExist(err) {
			result.Status = "missing"
			result.Message = "Gitが見つかりません。Gitをインストールし、アプリを再起動してから再確認してください。"
		} else {
			result.Message = "Gitを安全に起動できません。Gitのインストール先とPATHを確認してください。"
		}
		return result
	}
	result.Path = path
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	if platform == "darwin" && filepath.ToSlash(filepath.Clean(resolved)) == "/usr/bin/git" {
		// Apple's Git stub can open an installation dialog by itself. Query the
		// selected developer directory first; xcode-select -p is read-only.
		developerDir, err := run(ctx, "/usr/bin/xcode-select", "-p")
		if ctx.Err() != nil {
			result.Message = "Gitの確認を完了できませんでした。Gitのインストール状態を確認し、もう一度お試しください。"
			return result
		}
		info, statErr := os.Stat(strings.TrimSpace(developerDir))
		if err != nil || statErr != nil || !info.IsDir() {
			result.Status = "missing"
			result.Message = "Gitを使うためのCommand Line Toolsが見つかりません。Command Line ToolsまたはGitをインストールし、アプリを再起動してから再確認してください。"
			return result
		}
	}
	output, err := run(ctx, path, "--version")
	if ctx.Err() != nil {
		result.Message = "Gitの確認を完了できませんでした。Gitのインストール状態を確認し、もう一度お試しください。"
		return result
	}
	version := strings.TrimSpace(output)
	if err != nil || !gitVersionLine.MatchString(version) {
		result.Message = "Gitは見つかりましたが正常に起動できません。Gitのインストール状態とPATHを確認してください。"
		return result
	}
	result.Available = true
	result.Status = "ready"
	result.Version = version
	result.Message = "Gitを利用できます。"
	return result
}

func gitInstallationCommand(ctx context.Context, executable string, args ...string) (string, error) {
	cmd, err := runtimeCommand(ctx, executable, args...)
	if err != nil {
		return "", err
	}
	cmd.WaitDelay = 250 * time.Millisecond
	stdout := limitedBuffer{max: gitInstallationOutputLimit}
	stderr := limitedBuffer{max: gitInstallationOutputLimit}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	if stdout.truncated || stderr.truncated {
		return "", fmt.Errorf("Git installation check output exceeds limit")
	}
	return stdout.String(), nil
}
