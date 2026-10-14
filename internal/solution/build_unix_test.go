//go:build !windows

package solution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"onebyone/internal/model"
)

func fakeBuilder(t *testing.T, script string) (string, model.SolutionSettings, string) {
	t.Helper()
	root := t.TempDir()
	fixtureSolution(t, root, "App.vbproj")
	executable := fixtureFile(t, root, "fake-msbuild", "#!/bin/sh\n"+script)
	if err := os.Chmod(executable, 0755); err != nil {
		t.Fatal(err)
	}
	return root, model.SolutionSettings{Path: "App.sln", BuildTimeoutSeconds: 5}, executable
}

func TestRunBuildDistinguishesCompilerFailureAndLaunchFailure(t *testing.T) {
	root, settings, executable := fakeBuilder(t, "echo 'Main.vb(7,2): error BC30002: Unknown type'\nexit 1\n")
	result, err := runBuild(context.Background(), root, settings, executable)
	if err != nil || result.Success || result.ExitCode != 1 || len(result.Diagnostics) != 1 || result.StartedAt == "" || result.FinishedAt == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	result, err = runBuild(context.Background(), root, settings, filepath.Join(root, "missing"))
	if err == nil || result.Success || result.ExitCode != -1 {
		t.Fatalf("launch result=%#v err=%v", result, err)
	}
}

func TestRunBuildSuccessAndCancellation(t *testing.T) {
	root, settings, executable := fakeBuilder(t, "exit 0\n")
	result, err := runBuild(context.Background(), root, settings, executable)
	if err != nil || !result.Success || result.ExitCode != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	// The child inherits stdout; returning promptly also verifies that the
	// cancellation kills the process group rather than leaving its pipe open.
	root, settings, executable = fakeBuilder(t, "sleep 20 &\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err = runBuild(ctx, root, settings, executable)
	if !errors.Is(err, context.DeadlineExceeded) || result.Success {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("cancellation left a child process holding the output pipe")
	}
}

func TestRunBuildRefusesZeroExitWithCompilerErrors(t *testing.T) {
	root, settings, executable := fakeBuilder(t, "echo 'Main.vb(7,2): error BC30002: Unknown type'\nexit 0\n")
	result, err := runBuild(context.Background(), root, settings, executable)
	if err != nil || result.Success || result.ExitCode != 0 || len(result.Diagnostics) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
