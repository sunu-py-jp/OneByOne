package solution

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/japanese"
	"onebyone/internal/model"
)

func TestBuildArgumentsAreFixedAndRejectPropertyInjection(t *testing.T) {
	root := t.TempDir()
	fixtureSolution(t, root, "App.vbproj")
	settings := model.SolutionSettings{Path: "App.sln", Configuration: "Release", Platform: "Any CPU"}
	args, err := buildArguments(root, settings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, "\n"), "/p:Platform=Any CPU") || !strings.Contains(strings.Join(args, "\n"), "/t:Rebuild\n/restore") {
		t.Fatalf("args: %#v", args)
	}
	for _, value := range []string{"Debug;Other=true", "Debug,Other=true", "Debug\n/t:Clean", "Debug%3bOther=true", "Debug\" /t:Clean", "$(Configuration)", "Debug=Other"} {
		settings.Configuration = value
		if _, err := buildArguments(root, settings); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestBuildDiagnosticsParseDeduplicateAndResolveProjectRelativePaths(t *testing.T) {
	root, err := canonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sln := fixtureFile(t, root, "App.sln", "")
	project := fixtureFile(t, root, "src/App.vbproj", "")
	source := fixtureFile(t, root, "src/Main.vb", "")
	capture := &buildCapture{root: root, solution: sln, seen: map[string]bool{}}
	line := source + "(42,9): error BC30002: Type 'Missing' is not defined. [" + project + "]\r\n"
	input := "1>" + line + line + "Main.vb(3): error BC30451: Name is not declared. [" + project + "]\n" +
		project + ": error MSB3644: Reference assemblies missing.\n" +
		"vbc : error BC2008: No input sources specified. [" + project + "]\n" +
		source + "(1): warning BC40000: warning must not be an error.\n"
	for _, char := range []byte(input) {
		capture.Write([]byte{char})
	}
	capture.finish()
	expected := []model.SourceDiagnostic{
		{File: "src/Main.vb", Line: 42, Code: "BC30002", Message: "Type 'Missing' is not defined."},
		{File: "src/Main.vb", Line: 3, Code: "BC30451", Message: "Name is not declared."},
		{File: "src/App.vbproj", Code: "MSB3644", Message: "Reference assemblies missing."},
		{File: "src/App.vbproj", Code: "BC2008", Message: "No input sources specified."},
	}
	if !reflect.DeepEqual(capture.diagnostics, expected) {
		t.Fatalf("diagnostics: %#v", capture.diagnostics)
	}
}

func TestBuildDiagnosticsSurviveLogLimitAndDecodeShiftJIS(t *testing.T) {
	root, err := canonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	capture := &buildCapture{root: root, solution: filepath.Join(root, "App.sln"), seen: map[string]bool{}}
	for range 1025 {
		capture.Write(append(bytes.Repeat([]byte{'x'}, 2047), '\n'))
	}
	line, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte("処理.vb(2,3): error BC30002: 未定義の型です。\n"))
	if err != nil {
		t.Fatal(err)
	}
	capture.Write(line)
	capture.finish()
	if len(capture.diagnostics) != 1 || capture.diagnostics[0].File != "処理.vb" || capture.diagnostics[0].Message != "未定義の型です。" {
		t.Fatalf("diagnostics: %#v", capture.diagnostics)
	}
	if !capture.truncated || len(capture.String()) > maxBuildOutput+1024 {
		t.Fatal("log cap failed")
	}
}

func TestBuildDiagnosticResourceLimitsAndOutsidePaths(t *testing.T) {
	root, err := canonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	capture := &buildCapture{root: root, solution: filepath.Join(root, "App.sln"), seen: map[string]bool{}}
	capture.Write(bytes.Repeat([]byte{'x'}, maxDiagnosticLine+100))
	capture.Write([]byte("\n../../outside.vb(5): error BC30002: Error\n"))
	capture.finish()
	if !capture.diagnosticTruncated || len(capture.diagnostics) != 1 || capture.diagnostics[0].File != "App.sln" {
		t.Fatalf("capture: %#v", capture.diagnostics)
	}
}
