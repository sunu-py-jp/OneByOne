package catalog

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFindRGFromPackagedExecutableWithoutPATH(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	for _, layout := range []string{"cli-bin", "app-bin", "app-resources", "symlink-launcher", "non-executable-neighbor", "explicit-override", "missing"} {
		t.Run(layout, func(t *testing.T) {
			if layout == "non-executable-neighbor" && runtime.GOOS == "windows" {
				t.Skip("Windows does not use Unix executable mode bits")
			}
			root := t.TempDir()
			appDir := filepath.Join(root, "OneByOne.app", "Contents", "MacOS")
			if layout == "cli-bin" {
				appDir = filepath.Join(root, "distribution")
			}
			application := filepath.Join(appDir, "catalog-fixture"+extension)
			writeExecutableFixture(t, application, binary)
			bundled := filepath.Join(appDir, "bin", "rg"+extension)
			if layout == "app-resources" {
				bundled = filepath.Join(appDir, "..", "Resources", "rg"+extension)
			}
			if layout != "missing" {
				writeExecutableFixture(t, bundled, binary)
			}
			configured := ""
			if layout == "explicit-override" {
				configured = filepath.Join(root, "custom", "rg"+extension)
				writeExecutableFixture(t, configured, binary)
				bundled = configured
			}
			if layout == "non-executable-neighbor" {
				if err := os.WriteFile(filepath.Join(appDir, "rg"), []byte("not executable"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if layout == "symlink-launcher" {
				launcher := filepath.Join(root, "launcher"+extension)
				if err := os.Symlink(application, launcher); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				application = launcher
			}
			if layout == "missing" {
				bundled = "missing"
			}
			cmd := exec.Command(application, "-test.run=^TestFindRGPackagedProcessHelper$", "-test.count=1")
			// Exercise the real os.Executable resolver without system-installed
			// tools. In particular, no injected executable path hides a failure
			// to locate tools next to the running desktop app or CLI.
			cmd.Env = append(os.Environ(), "PATH=", "ONEBYONE_RG_EXPECTED="+bundled, "ONEBYONE_RG_CONFIGURED="+configured)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("packaged resolver failed: %v\n%s", err, output)
			}
		})
	}
}

func writeExecutableFixture(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0755); err != nil {
		t.Fatal(err)
	}
}

func TestFindRGPackagedProcessHelper(t *testing.T) {
	expected := os.Getenv("ONEBYONE_RG_EXPECTED")
	if expected == "" {
		t.Skip("subprocess fixture")
	}
	resolved, err := findRG(os.Getenv("ONEBYONE_RG_CONFIGURED"))
	if expected == "missing" {
		if err == nil || strings.Contains(err.Error(), "rgPath") || !strings.Contains(err.Error(), "再インストール") {
			t.Fatalf("missing-bundle guidance is unsuitable for the desktop UI: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(expected)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(resolved)
	if err != nil || !os.SameFile(want, got) {
		t.Fatalf("resolved %q instead of the expected bundled executable: %v", resolved, err)
	}
	output, err := exec.Command(resolved, "-test.run=^TestBundledRGFixtureCommand$", "-test.count=1").CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("bundled-rg-fixture-executed")) {
		t.Fatalf("resolved bundled file could not be executed: %v\n%s", err, output)
	}
}

func TestBundledRGFixtureCommand(t *testing.T) {
	if os.Getenv("ONEBYONE_RG_EXPECTED") == "" {
		t.Skip("subprocess executable fixture")
	}
	fmt.Println("bundled-rg-fixture-executed")
}
