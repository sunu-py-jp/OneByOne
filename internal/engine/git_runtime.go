package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var errBundledGit = errors.New("同梱のGitを利用できません。配布ファイル一式を再展開するか、OneByOneを再インストールしてから再起動してください")

// A packaged application owns the whole Git installation, not only its entry
// point. Keep helpers, templates and child-process lookup within that bundle.
type gitRuntime struct {
	Path, Root, ExecPath, TemplatePath string
	PathDirs                           []string
}

func bundledGitRuntime() (gitRuntime, error) {
	executable, err := os.Executable()
	if err != nil {
		return gitRuntime{}, err
	}
	return gitBundleAt(executable, runtime.GOOS)
}

func gitBundleAt(executable, platform string) (gitRuntime, error) {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	tools := filepath.Join(filepath.Dir(executable), "tools")
	root := filepath.Join(tools, "git")
	_, markerErr := os.Lstat(filepath.Join(tools, "runtime.json"))
	_, rootErr := os.Lstat(root)
	if os.IsNotExist(markerErr) && os.IsNotExist(rootErr) {
		// go test / development runs do not have a packaged runtime.
		return gitRuntime{}, nil
	}
	rt := gitRuntime{Root: root}
	fail := func() (gitRuntime, error) { return rt, errBundledGit }
	if rootErr != nil || (markerErr != nil && !os.IsNotExist(markerErr)) {
		return fail()
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fail()
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fail()
	}
	rt.Root = root
	bin, prefix := filepath.Join(root, "bin", "git"), root
	if platform == "windows" {
		prefix = ""
		for _, architecture := range []string{"clangarm64", "mingw64", "mingwarm64", "mingw32"} {
			candidate := filepath.Join(root, architecture)
			if stat, e := os.Stat(filepath.Join(candidate, "libexec", "git-core")); e == nil && stat.IsDir() {
				prefix = candidate
				break
			}
		}
		if prefix == "" {
			return fail()
		}
		bin = filepath.Join(root, "cmd", "git.exe")
		if _, err = os.Lstat(bin); os.IsNotExist(err) {
			bin = filepath.Join(prefix, "bin", "git.exe")
		}
	}
	rt.Path = bin
	for _, path := range []string{bin, filepath.Join(prefix, "libexec", "git-core"), filepath.Join(prefix, "share", "git-core", "templates")} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fail()
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil || !filepath.IsLocal(relative) {
			return fail()
		}
		info, err := os.Stat(resolved)
		if err != nil || (path == bin && !info.Mode().IsRegular()) || (path != bin && !info.IsDir()) {
			return fail()
		}
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fail()
	}
	rt.ExecPath = filepath.Join(prefix, "libexec", "git-core")
	rt.TemplatePath = filepath.Join(prefix, "share", "git-core", "templates")
	rt.PathDirs = []string{filepath.Dir(bin), filepath.Join(prefix, "bin"), rt.ExecPath}
	if platform == "windows" {
		rt.PathDirs = append(rt.PathDirs, filepath.Join(root, "usr", "bin"))
	}
	return rt, nil
}

func resolveGitRuntime() (gitRuntime, error) {
	rt, err := bundledGitRuntime()
	if err != nil || rt.Root != "" {
		return rt, err
	}
	rt.Path, err = exec.LookPath("git")
	return rt, err
}

func runtimeCommand(ctx context.Context, executable string, args ...string) (*exec.Cmd, error) {
	rt, err := bundledGitRuntime()
	if err != nil {
		return nil, err
	}
	name := filepath.Base(executable)
	if rt.Root != "" && (strings.EqualFold(name, "git") || strings.EqualFold(name, "git.exe")) {
		executable = rt.Path
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = gitCommandEnvironment(os.Environ(), rt)
	configureProcess(cmd)
	return cmd, nil
}

func gitCommandEnvironment(source []string, rt gitRuntime) []string {
	environment := []string{}
	path := ""
	for _, entry := range source {
		key, value, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "GIT_") || key == "AZURE_OPENAI_API_KEY" || key == "AZURE_OPENAI_AUTH_TOKEN" || key == "OPENAI_API_KEY" || key == "ANTHROPIC_API_KEY" {
			continue
		}
		if key == "PATH" && rt.Root != "" {
			path = value
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	if rt.Root != "" {
		parts := append([]string{}, rt.PathDirs...)
		if path != "" {
			parts = append(parts, path)
		}
		environment = append(environment, "PATH="+strings.Join(parts, string(os.PathListSeparator)), "GIT_EXEC_PATH="+rt.ExecPath, "GIT_TEMPLATE_DIR="+rt.TemplatePath)
	}
	return environment
}
