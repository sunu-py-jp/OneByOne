package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGitInstallationMissingFromPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	result := CheckGitInstallation(context.Background())
	if result.Available || result.Status != "missing" || result.Path != "" || !strings.Contains(result.Message, "インストール") {
		t.Fatalf("unexpected installation state: %+v", result)
	}
}

func TestGitInstallationVersionResults(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		err          error
		ready        bool
	}{
		{name: "standard", output: "git version 2.50.1\n", ready: true},
		{name: "Windows", output: "git version 2.50.1.windows.1\r\n", ready: true},
		{name: "Apple", output: "git version 2.50.1 (Apple Git-155)\n", ready: true},
		{name: "nonzero", output: "git version 2.50.1", err: errors.New("exit status 1")},
		{name: "invalid", output: "some other executable"},
		{name: "empty", output: ""},
		{name: "multiple lines", output: "git version 2.50.1\nUnexpected message"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := checkGitInstallation(context.Background(), "windows", func(name string) (string, error) {
				if name != "git" {
					t.Fatalf("lookup = %q", name)
				}
				return "C:/tools/git.exe", nil
			}, func(ctx context.Context, path string, args ...string) (string, error) {
				if path != "C:/tools/git.exe" || len(args) != 1 || args[0] != "--version" {
					t.Fatalf("unexpected command: %s %v", path, args)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("version check must have a short deadline")
				}
				return tc.output, tc.err
			})
			if result.Available != tc.ready || result.Platform != "windows" {
				t.Fatalf("unexpected installation state: %+v", result)
			}
			if tc.ready {
				if result.Status != "ready" || result.Version != strings.TrimSpace(tc.output) {
					t.Fatalf("unexpected ready state: %+v", result)
				}
			} else if result.Status != "unusable" || result.Version != "" {
				t.Fatalf("unexpected failure state: %+v", result)
			}
		})
	}
}

func TestGitInstallationRejectsUnsafePath(t *testing.T) {
	result := checkGitInstallation(context.Background(), "windows", func(string) (string, error) {
		return "git.exe", exec.ErrDot
	}, func(context.Context, string, ...string) (string, error) {
		t.Fatal("unsafe executable must not run")
		return "", nil
	})
	if result.Status != "unusable" || result.Available {
		t.Fatalf("unexpected state: %+v", result)
	}
}

func TestGitInstallationGuardsFolderAndDemo(t *testing.T) {
	for _, status := range []string{"missing", "unusable"} {
		t.Run(status, func(t *testing.T) {
			if status == "unusable" && runtime.GOOS == "windows" {
				t.Skip("failure executable uses a POSIX shell; probe failures are tested portably above")
			}
			s, source := workspaceTestService(t)
			parent, bin := t.TempDir(), t.TempDir()
			message := "Gitが見つかりません"
			if status == "unusable" {
				gitPath := filepath.Join(bin, "git")
				if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
				message = "Gitは見つかりましたが正常に起動できません"
			}
			t.Setenv("PATH", bin)
			if _, err := sourceGitRepository(context.Background(), source); err == nil || !strings.Contains(err.Error(), message) {
				t.Fatalf("source check lost installation error: %v", err)
			}
			if _, err := s.CreateDemoProject(parent); err == nil || !strings.Contains(err.Error(), message) {
				t.Fatalf("demo check lost installation error: %v", err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("demo created files before checking Git: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestGitInstallationAppleStubDoesNotStartInstaller(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(fmt.Sprint(installed), func(t *testing.T) {
			developerDir := filepath.Join(t.TempDir(), "Developer")
			if installed {
				if err := os.Mkdir(developerDir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			commands := []string{}
			result := checkGitInstallation(context.Background(), "darwin", func(string) (string, error) {
				return "/usr/bin/git", nil
			}, func(ctx context.Context, path string, args ...string) (string, error) {
				commands = append(commands, path+" "+strings.Join(args, " "))
				if path == "/usr/bin/xcode-select" {
					return developerDir + "\n", nil
				}
				return "git version 2.50.1 (Apple Git-155)", nil
			})
			if result.Available != installed || commands[0] != "/usr/bin/xcode-select -p" {
				t.Fatalf("state=%+v, commands=%v", result, commands)
			}
			if !installed && (result.Status != "missing" || len(commands) != 1) {
				t.Fatalf("must not invoke installer stub: state=%+v commands=%v", result, commands)
			}
			if installed && (len(commands) != 2 || commands[1] != "/usr/bin/git --version") {
				t.Fatalf("must probe installed Git: commands=%v", commands)
			}
		})
	}
}

func TestGitInstallationCommandBoundsAndIsolation(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "test-secret")
	t.Setenv("OPENAI_API_KEY", "test-secret")
	t.Setenv("ANTHROPIC_API_KEY", "test-secret")
	t.Setenv("GIT_DIR", "unrelated-repository")
	for _, mode := range []string{"ready", "nonzero", "oversized", "hang"} {
		t.Run(mode, func(t *testing.T) {
			timeout := 3 * time.Second
			if mode == "hang" {
				timeout = 200 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			started := time.Now()
			output, err := gitInstallationCommand(ctx, os.Args[0], "-test.run=^TestGitInstallationHelperProcess$", "--", "onebyone-git-check", mode)
			if mode == "ready" {
				if err != nil || strings.TrimSpace(output) != "git version 2.50.1" {
					t.Fatalf("output=%q err=%v", output, err)
				}
			} else if err == nil || output != "" {
				t.Fatalf("failure must not expose command output: output=%q err=%v", output, err)
			}
			if mode == "hang" && time.Since(started) > 3*time.Second {
				t.Fatal("Git probe did not stop promptly")
			}
		})
	}
}

func TestGitInstallationHelperProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "onebyone-git-check" || i+1 >= len(os.Args) {
			continue
		}
		if os.Getenv("AZURE_OPENAI_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("GIT_DIR") != "" {
			os.Exit(20)
		}
		switch os.Args[i+1] {
		case "ready":
			fmt.Println("git version 2.50.1")
		case "nonzero":
			fmt.Fprintln(os.Stderr, "private diagnostic")
			os.Exit(1)
		case "oversized":
			fmt.Print(strings.Repeat("x", gitInstallationOutputLimit+1))
		case "hang":
			time.Sleep(30 * time.Second)
		}
		os.Exit(0)
	}
}
