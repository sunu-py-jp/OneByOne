package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func gitRuntimeFixture(t *testing.T, directory, platform string, binary []byte) (string, string) {
	t.Helper()
	root := filepath.Join(directory, "tools", "git")
	prefix, executable := root, filepath.Join(root, "bin", "git")
	if platform == "windows" {
		prefix, executable = filepath.Join(root, "mingw64"), filepath.Join(root, "cmd", "git.exe")
	}
	for _, path := range []string{filepath.Dir(executable), filepath.Join(prefix, "libexec", "git-core"), filepath.Join(prefix, "share", "git-core", "templates")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(executable, binary, 0755); err != nil {
		t.Fatal(err)
	}
	return root, executable
}

func TestBundledGitLayoutsAndIncompleteRuntime(t *testing.T) {
	platforms := []string{runtime.GOOS}
	if runtime.GOOS != "windows" {
		platforms = append(platforms, "windows")
	}
	for _, platform := range platforms {
		for _, condition := range []string{"complete", "alternate-windows-entry", "missing-binary", "missing-helpers", "missing-templates", "marker-without-git"} {
			t.Run(platform+"/"+condition, func(t *testing.T) {
				dir := t.TempDir()
				root, binary := gitRuntimeFixture(t, dir, platform, []byte("fixture"))
				prefix := root
				if platform == "windows" {
					prefix = filepath.Join(root, "mingw64")
				}
				switch condition {
				case "alternate-windows-entry":
					if platform != "windows" {
						t.Skip("Windows alternate entry")
					}
					alternate := filepath.Join(prefix, "bin", "git.exe")
					writeTest(t, alternate, []byte("fixture"))
					if err := os.Chmod(alternate, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(binary); err != nil {
						t.Fatal(err)
					}
					binary = alternate
				case "missing-binary":
					if err := os.Remove(binary); err != nil {
						t.Fatal(err)
					}
				case "missing-helpers":
					if err := os.RemoveAll(filepath.Join(prefix, "libexec")); err != nil {
						t.Fatal(err)
					}
				case "missing-templates":
					if err := os.RemoveAll(filepath.Join(prefix, "share")); err != nil {
						t.Fatal(err)
					}
				case "marker-without-git":
					writeTest(t, filepath.Join(dir, "tools", "runtime.json"), []byte(`{"version":1}`))
					if err := os.RemoveAll(root); err != nil {
						t.Fatal(err)
					}
				}
				rt, err := gitBundleAt(filepath.Join(dir, "OneByOne"), platform)
				if condition != "complete" && condition != "alternate-windows-entry" {
					if !errors.Is(err, errBundledGit) || rt.Root == "" {
						t.Fatalf("incomplete package could fall back to system Git: %+v %v", rt, err)
					}
					return
				}
				if err != nil || !sameRoot(rt.Path, binary) || !sameRoot(rt.ExecPath, filepath.Join(prefix, "libexec", "git-core")) || !sameRoot(rt.TemplatePath, filepath.Join(prefix, "share", "git-core", "templates")) {
					t.Fatalf("unexpected runtime: %+v %v", rt, err)
				}
			})
		}
	}
	if rt, err := gitBundleAt(filepath.Join(t.TempDir(), "development"), runtime.GOOS); err != nil || rt.Root != "" {
		t.Fatalf("development binary should retain PATH lookup: %+v %v", rt, err)
	}
}

func TestBundledGitCannotFollowExternalExecutable(t *testing.T) {
	dir := t.TempDir()
	_, binary := gitRuntimeFixture(t, dir, runtime.GOOS, []byte("fixture"))
	outside := filepath.Join(t.TempDir(), "git")
	writeTest(t, outside, []byte("outside"))
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, binary); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := gitBundleAt(filepath.Join(dir, "OneByOne"), runtime.GOOS); !errors.Is(err, errBundledGit) {
		t.Fatalf("bundle followed an external executable: %v", err)
	}
}

func TestBundledGitWindowsARM64UsesClangLayout(t *testing.T) {
	for _, missingTemplates := range []bool{false, true} {
		t.Run(fmt.Sprint("missing-templates=", missingTemplates), func(t *testing.T) {
			dir := t.TempDir()
			root, binary := gitRuntimeFixture(t, dir, "windows", []byte("fixture"))
			prefix := filepath.Join(root, "clangarm64")
			if err := os.Rename(filepath.Join(root, "mingw64"), prefix); err != nil {
				t.Fatal(err)
			}
			if missingTemplates {
				if err := os.RemoveAll(filepath.Join(prefix, "share", "git-core", "templates")); err != nil {
					t.Fatal(err)
				}
			}
			rt, err := gitBundleAt(filepath.Join(dir, "OneByOne.exe"), "windows")
			if missingTemplates {
				if !errors.Is(err, errBundledGit) || rt.Root == "" {
					t.Fatalf("incomplete ARM64 bundle may fall back to external Git: %+v %v", rt, err)
				}
				return
			}
			if err != nil || !sameRoot(rt.Path, binary) || !sameRoot(rt.ExecPath, filepath.Join(prefix, "libexec", "git-core")) || !sameRoot(rt.TemplatePath, filepath.Join(prefix, "share", "git-core", "templates")) {
				t.Fatalf("ARM64 bundle did not select clangarm64: %+v %v", rt, err)
			}
			environment := gitCommandEnvironment([]string{"PATH=external-git", "GIT_EXEC_PATH=external-helper"}, rt)
			for _, expected := range []string{"GIT_EXEC_PATH=" + rt.ExecPath, "GIT_TEMPLATE_DIR=" + rt.TemplatePath} {
				found := false
				for _, item := range environment {
					if item == expected {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing ARM64 runtime environment %q: %v", expected, environment)
				}
			}
			if len(rt.PathDirs) < 2 || !sameRoot(rt.PathDirs[1], filepath.Join(prefix, "bin")) {
				t.Fatalf("ARM64 helper executable directory missing from PATH: %v", rt.PathDirs)
			}
		})
	}
}

func TestBundledGitUsedForProbeCommandsChecksAndFileListing(t *testing.T) {
	binary, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	for _, condition := range []string{"complete", "incomplete", "marker-only", "symlink-launcher"} {
		t.Run(condition, func(t *testing.T) {
			dir := t.TempDir()
			app := filepath.Join(dir, "engine-fixture"+ext)
			if err := os.WriteFile(app, binary, 0755); err != nil {
				t.Fatal(err)
			}
			root, _ := gitRuntimeFixture(t, dir, runtime.GOOS, binary)
			if condition == "incomplete" {
				if err := os.RemoveAll(filepath.Join(root, "bin")); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(root, "cmd")); err != nil {
					t.Fatal(err)
				}
			}
			if condition == "marker-only" {
				writeTest(t, filepath.Join(dir, "tools", "runtime.json"), []byte(`{"version":1}`))
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
			}
			if condition == "symlink-launcher" {
				launcher := filepath.Join(t.TempDir(), "launcher"+ext)
				if err := os.Symlink(app, launcher); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				app = launcher
			}
			decoy := t.TempDir()
			if err := os.WriteFile(filepath.Join(decoy, "git"+ext), binary, 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(app, "-test.run=^TestBundledGitRuntimeProcessHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "ONEBYONE_BUNDLED_GIT_FIXTURE=1", "ONEBYONE_GIT_RUNTIME_CONDITION="+condition, "PATH="+decoy, "GIT_EXEC_PATH=hostile-helper", "GIT_TEMPLATE_DIR=hostile-template", "GIT_DIR=unrelated-repo", "OPENAI_API_KEY=test-secret")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bundled runtime failed: %v\n%s", err, output)
			}
		})
	}
}

func TestBundledGitRuntimeProcessHelper(t *testing.T) {
	condition := os.Getenv("ONEBYONE_GIT_RUNTIME_CONDITION")
	if condition == "" {
		t.Skip("subprocess fixture")
	}
	ctx := context.Background()
	installation := CheckGitInstallation(ctx)
	if condition == "incomplete" || condition == "marker-only" {
		if installation.Available || installation.Status != "bundle_error" || !strings.Contains(installation.Message, "再インストール") {
			t.Fatalf("broken bundle used system Git: %+v", installation)
		}
		if _, err := git(ctx, t.TempDir(), "fixture-environment"); !errors.Is(err, errBundledGit) {
			t.Fatalf("command fell back after broken bundle: %v", err)
		}
		return
	}
	if !installation.Available {
		t.Fatalf("bundled Git failed probe: %+v", installation)
	}
	rt, err := resolveGitRuntime()
	if err != nil || rt.Root == "" {
		t.Fatalf("missing runtime: %+v %v", rt, err)
	}
	assertOutput := func(output string, err error) {
		t.Helper()
		var value map[string]string
		if err != nil || json.Unmarshal([]byte(output), &value) != nil {
			t.Fatalf("command failed: %q %v", output, err)
		}
		if !sameRoot(value["executable"], rt.Path) || value["GIT_EXEC_PATH"] != rt.ExecPath || value["GIT_TEMPLATE_DIR"] != rt.TemplatePath || value["GIT_DIR"] != "" || value["OPENAI_API_KEY"] != "" || !strings.HasPrefix(value["PATH"], filepath.Dir(rt.Path)+string(os.PathListSeparator)) {
			t.Fatalf("command escaped bundled runtime or exposed inherited settings: %+v", value)
		}
	}
	root := t.TempDir()
	assertOutput(git(ctx, root, "fixture-environment"))
	assertOutput(command(ctx, root, "git", "fixture-environment"))
	checks := runChecks(ctx, model.Config{CheckCommands: []model.Command{{Executable: "git", Args: []string{"fixture-environment"}}}}, root)
	if len(checks) != 1 || checks[0].Status != "passed" {
		t.Fatalf("validation did not use bundled Git: %+v", checks)
	}
	assertOutput(checks[0].Detail, nil)
	writeTest(t, filepath.Join(root, "tracked.txt"), []byte("source"))
	files, truncated, err := listTargetFiles(root, 10)
	if err != nil || truncated || len(files) != 1 || files[0].File != "tracked.txt" {
		t.Fatalf("listing did not use bundled Git: %+v %t %v", files, truncated, err)
	}
}

// Executable copies serve as a portable fake Git on both macOS and Windows.
// Intercept before testing flag parsing, because Git arguments are not Go flags.
func runBundledGitFixture() {
	if os.Getenv("ONEBYONE_BUNDLED_GIT_FIXTURE") != "1" {
		return
	}
	name := strings.ToLower(filepath.Base(os.Args[0]))
	if name != "git" && name != "git.exe" {
		return
	}
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--version":
			fmt.Println("git version 2.53.0")
			os.Exit(0)
		case "ls-files":
			fmt.Print("tracked.txt\x00")
			os.Exit(0)
		}
	}
	executable, _ := os.Executable()
	value := map[string]string{"executable": executable}
	for _, name := range []string{"PATH", "GIT_EXEC_PATH", "GIT_TEMPLATE_DIR", "GIT_DIR", "OPENAI_API_KEY"} {
		value[name] = os.Getenv(name)
	}
	_ = json.NewEncoder(os.Stdout).Encode(value)
	os.Exit(0)
}
