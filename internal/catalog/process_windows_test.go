//go:build windows

package catalog

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Exercise the actual rg command path with a console executable, so removing
// the background-process configuration makes this test fail on Windows.
func TestRGCommandRunsWithoutConsoleWindow(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(os.Args, "onebyone-detached-rg-test") {
		// Match the desktop app: the parent has no console to inherit. This
		// also makes the test independent of the terminal/SSH running it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestRGCommandRunsWithoutConsoleWindow$", "--", "onebyone-detached-rg-test")
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS, HideWindow: true}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("console-free parent failed: %v\n%s", err, output)
		}
		return
	}
	c := Catalog{rg: executable}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{"-test.run=^TestRGConsoleProbe$", "--", "onebyone-console-probe"}
	output, err := c.run(ctx, "", strings.NewReader("pattern input"), args...)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "window-free:pattern input" {
		t.Fatalf("unexpected command output: %q", output)
	}
	_, err = c.run(ctx, "", nil, append(args, "fail")...)
	if err == nil || !strings.Contains(err.Error(), "probe diagnostic") {
		t.Fatalf("command stderr was not preserved: %v", err)
	}
}

func TestRGConsoleProbe(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == "onebyone-console-probe" {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	window, _, _ := kernel.NewProc("GetConsoleWindow").Call()
	// CREATE_NO_WINDOW may still provide console services (including a code
	// page); it must not create a console window.
	if window != 0 {
		fmt.Fprintf(os.Stderr, "background command unexpectedly has a console window: %#x", window)
		os.Exit(2)
	}
	if marker+1 < len(os.Args) && os.Args[marker+1] == "fail" {
		fmt.Fprint(os.Stderr, "probe diagnostic")
		os.Exit(2)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("window-free:%s", input)
	os.Exit(0)
}
