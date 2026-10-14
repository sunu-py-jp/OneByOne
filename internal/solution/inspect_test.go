package solution

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/japanese"
)

func fixtureFile(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixtureSolution(t *testing.T, root string, projects ...string) {
	t.Helper()
	text := "Microsoft Visual Studio Solution File, Format Version 12.00\r\n# Visual Studio Version 17\r\n"
	for i, project := range projects {
		text += fmt.Sprintf("Project(\"{VB}\") = \"Project%d\", \"%s\", \"{GUID%d}\"\r\nEndProject\r\n", i, strings.ReplaceAll(project, "/", "\\"), i)
	}
	fixtureFile(t, root, "App.sln", text)
}

func TestInspectLegacyAndSDKWithLinkedSources(t *testing.T) {
	root := t.TempDir()
	fixtureSolution(t, root, "Legacy/Legacy.vbproj")
	fixtureFile(t, root, "Legacy/Legacy.vbproj", `<Project xmlns="http://schemas.microsoft.com/developer/msbuild/2003"><ItemGroup>
<Compile Include="Main.vb"/><Compile Include="..\Shared\Linked.vb"/>
<Compile Include="Generated.Designer.vb"><AutoGen>True</AutoGen></Compile>
<ProjectReference Include="..\SDK\SDK.vbproj"/>
</ItemGroup></Project>`)
	fixtureFile(t, root, "SDK/SDK.vbproj", `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>
<Compile Remove="Removed.vb"/><ProjectReference Include="..\Legacy\Legacy.vbproj"/>
</ItemGroup></Project>`)
	for _, name := range []string{"Legacy/Main.vb", "Legacy/Unused.vb", "Legacy/Generated.Designer.vb", "Shared/Linked.vb", "SDK/Class.vb", "SDK/sub/Nested.vb", "SDK/Removed.vb", "SDK/obj/Generated.vb", "SDK/bin/Other.vb", "SDK/Foo.g.vb"} {
		fixtureFile(t, root, name, "Class Example\nEnd Class\n")
	}
	info, err := Inspect(root, "App.sln")
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != "App.sln" {
		t.Fatalf("path: %s", info.Path)
	}
	if expected := []string{"Legacy/Legacy.vbproj", "SDK/SDK.vbproj"}; !reflect.DeepEqual(info.Projects, expected) {
		t.Fatalf("projects: %#v", info.Projects)
	}
	if expected := []string{"Legacy/Generated.Designer.vb", "Legacy/Main.vb", "SDK/Class.vb", "SDK/Foo.g.vb", "SDK/sub/Nested.vb", "Shared/Linked.vb"}; !reflect.DeepEqual(info.Files, expected) {
		t.Fatalf("files: %#v", info.Files)
	}
}

func TestInspectIncludesDesignerAndAutoGenCompilationInputs(t *testing.T) {
	for _, sdk := range []bool{false, true} {
		t.Run(fmt.Sprintf("sdk=%t", sdk), func(t *testing.T) {
			root := t.TempDir()
			fixtureSolution(t, root, "App/App.vbproj")
			project := `<Project><ItemGroup>
<Compile Include="Form1.vb"/><Compile Include="Form1.Designer.vb"><DependentUpon>Form1.vb</DependentUpon></Compile>
<Compile Include="My Project/Resources.Designer.vb"><AutoGen>True</AutoGen><DesignTime>True</DesignTime></Compile>
<Compile Include="CustomAutoGen.vb"><AutoGen>True</AutoGen></Compile>
<Compile Include="Model.g.vb"/><Compile Include="Model.g.i.vb"/><Compile Include="Model.generated.vb"/>
</ItemGroup></Project>`
			if sdk {
				project = `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><Compile Update="CustomAutoGen.vb"><AutoGen>True</AutoGen></Compile></ItemGroup></Project>`
			}
			fixtureFile(t, root, "App/App.vbproj", project)
			expected := []string{"App/CustomAutoGen.vb", "App/Form1.Designer.vb", "App/Form1.vb", "App/Model.g.i.vb", "App/Model.g.vb", "App/Model.generated.vb", "App/My Project/Resources.Designer.vb"}
			for _, name := range append(append([]string{}, expected...), "App/bin/Generated.Designer.vb", "App/obj/CustomAutoGen.vb", "App/obj/Debug/Generated.g.vb", "App/Form1.resx") {
				fixtureFile(t, root, name, "")
			}
			info, err := Inspect(root, "App.sln")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(info.Files, expected) {
				t.Fatalf("compilation inputs: %#v; want %#v", info.Files, expected)
			}
		})
	}
}

func TestInspectCompileGlobExcludeAndDisableDefaults(t *testing.T) {
	root := t.TempDir()
	fixtureSolution(t, root, "App/App.vbproj")
	fixtureFile(t, root, "App/App.vbproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><EnableDefaultCompileItems>false</EnableDefaultCompileItems></PropertyGroup><ItemGroup><Compile Include="code\**\*.vb;..\Shared\*.vb" Exclude="code\**\Skip.vb"/><Compile Remove="code\Removed.vb"/></ItemGroup></Project>`)
	for _, name := range []string{"App/code/Top.vb", "App/code/sub/Deep.vb", "App/code/sub/Skip.vb", "App/code/Removed.vb", "App/NotIncluded.vb", "Shared/Linked.vb"} {
		fixtureFile(t, root, name, "")
	}
	info, err := Inspect(root, "App.sln")
	if err != nil {
		t.Fatal(err)
	}
	if expected := []string{"App/code/Top.vb", "App/code/sub/Deep.vb", "Shared/Linked.vb"}; !reflect.DeepEqual(info.Files, expected) {
		t.Fatalf("files: %#v", info.Files)
	}
}

func TestInspectRejectsUnsafeInputs(t *testing.T) {
	for _, input := range []string{"../../outside.vb", `C:\outside.vb`, "obj/Generated.vb", "$(UnknownRoot)/File.vb", "@(OtherItems)", "File.vb:stream"} {
		t.Run(input, func(t *testing.T) {
			root := t.TempDir()
			fixtureSolution(t, root, "App/App.vbproj")
			fixtureFile(t, root, "App/App.vbproj", `<Project><ItemGroup><Compile Include="`+input+`"/></ItemGroup></Project>`)
			if _, err := Inspect(root, "App.sln"); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}

func TestInspectRejectsSymlinkFilesAndDirectories(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			root := t.TempDir()
			fixtureSolution(t, root, "App/App.vbproj")
			fixtureFile(t, root, "App/App.vbproj", `<Project Sdk="Microsoft.NET.Sdk"/>`)
			target := fixtureFile(t, root, "External/Source.vb", "")
			name := "App/Link.vb"
			if directory {
				target, name = filepath.Dir(target), "App/Link"
			}
			if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
				t.Skip(err)
			}
			if _, err := Inspect(root, "App.sln"); err == nil {
				t.Fatal("symlink accepted")
			}
		})
	}
}

func TestInspectSupportsShiftJISProjectNames(t *testing.T) {
	root := t.TempDir()
	fixtureSolution(t, root, "App.vbproj")
	path := fixtureFile(t, root, "App.vbproj", "")
	content, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte(`<?xml version="1.0" encoding="Shift_JIS"?><Project><ItemGroup><Compile Include="処理.vb"/></ItemGroup></Project>`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, root, "処理.vb", "")
	info, err := Inspect(root, "App.sln")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info.Files, []string{"処理.vb"}) {
		t.Fatalf("files: %#v", info.Files)
	}
}

func TestDiscoverAndInvalidSolution(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"App.sln", "sub/Other.SLN", "obj/Generated.sln", ".git/Hidden.sln"} {
		fixtureFile(t, root, name, "")
	}
	paths, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"App.sln", "sub/Other.SLN"}) {
		t.Fatalf("paths: %#v", paths)
	}
	if _, err := Inspect(root, "App.sln"); err == nil {
		t.Fatal("invalid sln accepted")
	}
	fixtureSolution(t, root, "App.csproj")
	if _, err := Inspect(root, "App.sln"); err == nil {
		t.Fatal("solution without VB accepted")
	}
}

func TestInspectUnicodeGlobsAndConditionalDefaults(t *testing.T) {
	root := t.TempDir()
	fixtureSolution(t, root, "App.vbproj")
	fixtureFile(t, root, "App.vbproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup Condition="'$(Configuration)' == 'Release'"><EnableDefaultCompileItems>false</EnableDefaultCompileItems></PropertyGroup><ItemGroup Condition="'$(Configuration)' == 'Release'"><Compile Remove="Default.vb"/></ItemGroup><ItemGroup><Compile Include="日本語\*.vb"/></ItemGroup></Project>`)
	fixtureFile(t, root, "日本語/処理.vb", "")
	fixtureFile(t, root, "Default.vb", "")
	info, err := Inspect(root, "App.sln")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info.Files, []string{"Default.vb", "日本語/処理.vb"}) {
		t.Fatalf("files: %#v", info.Files)
	}
}
