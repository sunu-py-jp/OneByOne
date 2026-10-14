// Package solution discovers the editable inputs of Visual Basic solutions and
// runs Visual Studio 2022 MSBuild. Project inspection never executes MSBuild.
package solution

import (
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"onebyone/internal/sourceencoding"
)

type Info struct {
	Path     string   `json:"path"`
	Projects []string `json:"projects"`
	Files    []string `json:"files"`
}

var solutionProject = regexp.MustCompile(`(?m)^\s*Project\("[^"\r\n]+"\)\s*=\s*"[^"\r\n]*"\s*,\s*"([^"\r\n]+)"`)

// Discover lists solution files without following symlinks or descending into
// build outputs, version control metadata, or dependency directories.
func Discover(root string) ([]string, error) {
	base, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if path != base && excludedDirectory(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".sln") {
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

// Inspect returns root-relative VB and related project text inputs. The .sln
// and .vbproj paths are
// exposed separately so callers can include them as editable project context.
// This is a conservative, static inspection: custom MSBuild expressions in
// item paths are rejected instead of silently selecting unrelated files.
// Designer/AutoGen sources are compilation inputs too. The execution layer
// separately requires every editable input to exist in its pinned Git snapshot.
func Inspect(root, solutionPath string) (Info, error) {
	info := Info{Projects: []string{}, Files: []string{}}
	base, err := canonicalRoot(root)
	if err != nil {
		return info, err
	}
	path, err := safePath(base, base, solutionPath)
	if err != nil {
		return info, err
	}
	if !strings.EqualFold(filepath.Ext(path), ".sln") {
		return info, fmt.Errorf("Visual Studio 2022の.slnファイルを選択してください")
	}
	text, err := readText(path)
	if err != nil {
		return info, err
	}
	info.Path = relative(base, path)
	if !strings.Contains(text, "Microsoft Visual Studio Solution File, Format Version 12.00") {
		return info, fmt.Errorf("Visual Studio形式の.slnファイルではありません: %s", info.Path)
	}
	queue := []string{}
	for _, match := range solutionProject.FindAllStringSubmatch(text, -1) {
		extension := strings.ToLower(filepath.Ext(match[1]))
		if !strings.HasSuffix(extension, "proj") {
			continue
		}
		project, err := safePath(base, filepath.Dir(path), match[1])
		if err != nil {
			return info, err
		}
		if extension == ".vbproj" {
			queue = append(queue, project)
		}
	}
	if len(queue) == 0 {
		return info, fmt.Errorf(".slnにVB.NETの.vbprojが含まれていません")
	}
	seenProjects, seenFiles := map[string]bool{}, map[string]bool{}
	for len(queue) > 0 {
		project := queue[0]
		queue = queue[1:]
		key := strings.ToLower(project)
		if seenProjects[key] {
			continue
		}
		seenProjects[key] = true
		files, references, err := inspectProject(base, project)
		if err != nil {
			return info, fmt.Errorf("%s: %w", relative(base, project), err)
		}
		info.Projects = append(info.Projects, relative(base, project))
		queue = append(queue, references...)
		for _, file := range files {
			rel := relative(base, file)
			if !seenFiles[strings.ToLower(rel)] {
				seenFiles[strings.ToLower(rel)] = true
				info.Files = append(info.Files, rel)
			}
		}
	}
	sort.Strings(info.Projects)
	sort.Strings(info.Files)
	return info, nil
}

type projectItem struct {
	XMLName   xml.Name
	Include   string `xml:"Include,attr"`
	Exclude   string `xml:"Exclude,attr"`
	Remove    string `xml:"Remove,attr"`
	Condition string `xml:"Condition,attr"`
	// Link and DependentUpon are display metadata, not physical input paths.
}

type projectXML struct {
	XMLName xml.Name `xml:"Project"`
	SDK     string   `xml:"Sdk,attr"`
	SDKs    []struct {
		Name string `xml:"Name,attr"`
	} `xml:"Sdk"`
	Properties []struct {
		Condition                          string `xml:"Condition,attr"`
		EnableDefaultItems                 string `xml:"EnableDefaultItems"`
		EnableDefaultCompileItems          string `xml:"EnableDefaultCompileItems"`
		EnableDefaultEmbeddedResourceItems string `xml:"EnableDefaultEmbeddedResourceItems"`
		EnableDefaultNoneItems             string `xml:"EnableDefaultNoneItems"`
		EnableDefaultPageItems             string `xml:"EnableDefaultPageItems"`
		EnableDefaultApplicationDefinition string `xml:"EnableDefaultApplicationDefinition"`
		UseWPF                             string `xml:"UseWPF"`
	} `xml:"PropertyGroup"`
	Items []struct {
		Condition string        `xml:"Condition,attr"`
		Entries   []projectItem `xml:",any"`
	} `xml:"ItemGroup"`
}

func inspectProject(root, path string) ([]string, []string, error) {
	text, err := readText(path)
	if err != nil {
		return nil, nil, err
	}
	var project projectXML
	// readText already decoded source bytes, including legacy Shift_JIS. The
	// declaration's original charset must not cause a second conversion.
	decoder := xml.NewDecoder(strings.NewReader(text))
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	if err := decoder.Decode(&project); err != nil {
		return nil, nil, fmt.Errorf("プロジェクトXMLを読み込めません: %w", err)
	}
	dir := filepath.Dir(path)
	defaultItems, defaultCompile, defaultResources, defaultNone := true, true, true, true
	defaultPages, defaultApplication, useWPF := true, true, false
	for _, props := range project.Properties {
		// Conditional false values cannot suppress another configuration's
		// inputs. Keep the union of potentially active project items.
		for _, setting := range []struct {
			value  string
			target *bool
		}{
			{props.EnableDefaultItems, &defaultItems},
			{props.EnableDefaultCompileItems, &defaultCompile},
			{props.EnableDefaultEmbeddedResourceItems, &defaultResources},
			{props.EnableDefaultNoneItems, &defaultNone},
			{props.EnableDefaultPageItems, &defaultPages},
			{props.EnableDefaultApplicationDefinition, &defaultApplication},
			{props.UseWPF, &useWPF},
		} {
			value := strings.TrimSpace(setting.value)
			if strings.EqualFold(value, "true") {
				*setting.target = true
			}
			if props.Condition == "" && strings.EqualFold(value, "false") {
				*setting.target = false
			}
		}
	}
	// Keep item types separate: removing a None item must not remove a Page,
	// Compile, or EmbeddedResource that references the same physical file.
	byKind := map[string]map[string]bool{}
	for _, kind := range []string{"Compile", "Page", "ApplicationDefinition", "EmbeddedResource", "Resource", "Content", "None", "AdditionalFiles", "PRIResource"} {
		byKind[kind] = map[string]bool{}
	}
	if (project.SDK != "" || len(project.SDKs) > 0) && defaultItems {
		matches, err := expandProjectItems(root, dir, "**/*")
		if err != nil {
			return nil, nil, err
		}
		for _, file := range matches {
			switch strings.ToLower(filepath.Ext(file)) {
			case ".vb":
				if defaultCompile {
					byKind["Compile"][file] = true
				}
			case ".resx":
				if defaultResources {
					byKind["EmbeddedResource"][file] = true
				}
			default:
				if defaultNone {
					byKind["None"][file] = true
				}
			}
			if useWPF && strings.EqualFold(filepath.Ext(file), ".xaml") {
				application := defaultApplication && strings.EqualFold(relative(dir, file), "Application.xaml")
				if application {
					byKind["ApplicationDefinition"][file] = true
				} else if defaultPages {
					byKind["Page"][file] = true
				}
				// This mirrors the Desktop SDK's default None removal.
				if defaultApplication && defaultPages {
					delete(byKind["None"], file)
				}
			}
		}
	}
	references := []string{}
	for _, group := range project.Items {
		for _, item := range group.Entries {
			kind := item.XMLName.Local
			if kind == "ProjectReference" {
				if item.Include == "" {
					continue
				}
				if unresolved(item.Include) {
					return nil, nil, fmt.Errorf("ProjectReferenceの動的なパスには対応していません: %s", item.Include)
				}
				refPath, err := safePath(root, dir, item.Include)
				if err != nil {
					return nil, nil, err
				}
				if strings.EqualFold(filepath.Ext(refPath), ".vbproj") {
					references = append(references, refPath)
				}
				continue
			}
			files, supported := byKind[kind]
			if !supported {
				continue
			}
			if item.Include != "" {
				matches, err := expandProjectItems(root, dir, item.Include)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", kind, err)
				}
				excluded, err := itemMatchers(root, dir, item.Exclude)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", kind, err)
				}
				for _, file := range matches {
					if kind == "Compile" && !strings.EqualFold(filepath.Ext(file), ".vb") {
						continue
					}
					if !matchesAny(excluded, filepath.ToSlash(file)) {
						files[file] = true
					}
				}
			}
			// Conditional removals must not omit files that may be active for
			// another configuration. They remain in the conservative union.
			if item.Remove != "" && item.Condition == "" && group.Condition == "" {
				removed, err := itemMatchers(root, dir, item.Remove)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", kind, err)
				}
				for file := range files {
					if matchesAny(removed, filepath.ToSlash(file)) {
						delete(files, file)
					}
				}
			}
		}
	}
	files := map[string]bool{}
	for _, inputs := range byKind {
		for file := range inputs {
			files[file] = true
		}
	}
	result := make([]string, 0, len(files))
	for file := range files {
		result = append(result, file)
	}
	sort.Strings(result)
	return result, references, nil
}

func unresolved(value string) bool {
	return strings.Contains(value, "$(") || strings.Contains(value, "@(") || strings.Contains(value, "%(") || strings.Contains(value, "%")
}

func itemMatchers(root, dir, value string) ([]*regexp.Regexp, error) {
	result := []*regexp.Regexp{}
	for _, item := range strings.Split(value, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if unresolved(item) {
			return nil, fmt.Errorf("項目の動的なパスには対応していません: %s", item)
		}
		pattern := strings.ReplaceAll(item, "\\", "/")
		if !filepath.IsAbs(filepath.FromSlash(pattern)) {
			pattern = filepath.ToSlash(filepath.Join(dir, filepath.FromSlash(pattern)))
		}
		if !insideRoot(root, filepath.FromSlash(pattern)) {
			return nil, fmt.Errorf("項目のパスがワークスペース外です: %s", item)
		}
		var expression strings.Builder
		expression.WriteString("(?i)^")
		characters := []rune(pattern)
		for i := 0; i < len(characters); i++ {
			switch characters[i] {
			case '*':
				if i+1 < len(characters) && characters[i+1] == '*' {
					i++
					if i+1 < len(characters) && characters[i+1] == '/' {
						i++
						expression.WriteString("(?:.*/)?")
					} else {
						expression.WriteString(".*")
					}
				} else {
					expression.WriteString("[^/]*")
				}
			case '?':
				expression.WriteString("[^/]")
			default:
				expression.WriteString(regexp.QuoteMeta(string(characters[i])))
			}
		}
		expression.WriteString("$")
		compiled, err := regexp.Compile(expression.String())
		if err != nil {
			return nil, err
		}
		result = append(result, compiled)
	}
	return result, nil
}

func matchesAny(patterns []*regexp.Regexp, path string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

func expandProjectItems(root, dir, value string) ([]string, error) {
	result := []string{}
	for _, item := range strings.Split(value, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		matchers, err := itemMatchers(root, dir, item)
		if err != nil {
			return nil, err
		}
		if !strings.ContainsAny(item, "*?") {
			path, err := safePath(root, dir, item)
			if err != nil {
				return nil, err
			}
			if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("項目には通常ファイルを指定してください: %s", item)
			}
			if editableProjectText(path) {
				result = append(result, path)
			}
			continue
		}
		// Start traversal before the first wildcard, allowing linked sources
		// in a sibling directory while keeping all reads inside the workspace.
		normalized := strings.ReplaceAll(item, "\\", "/")
		prefix := normalized[:strings.IndexAny(normalized, "*?")]
		baseName := filepath.FromSlash(prefix[:strings.LastIndex(prefix, "/")+1])
		if baseName == "" {
			baseName = "."
		}
		base, err := safePath(root, dir, baseName)
		if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("シンボリックリンクは修正対象にできません: %s", relative(root, path))
			}
			if d.IsDir() {
				if path != base && excludedDirectory(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() && editableProjectText(path) && matchesAny(matchers, filepath.ToSlash(path)) {
				result = append(result, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func editableProjectText(path string) bool {
	// Include related source/configuration text, not arbitrary project assets.
	// In particular images, assemblies, compiled resources and signing keys
	// must never be proposed as textual replacements. Generated-looking source
	// names remain editable when they are committed inputs outside bin/obj.
	switch strings.ToLower(filepath.Ext(path)) {
	case ".vb", ".xaml", ".resx", ".resw", ".config", ".xml", ".json",
		".settings", ".myapp", ".manifest", ".xsd", ".xsl", ".xslt", ".wsdl",
		".disco", ".discomap", ".svcmap", ".datasource", ".txt", ".md",
		".csv", ".yaml", ".yml", ".ini", ".sql", ".html", ".htm", ".css",
		".js", ".ts", ".vbhtml", ".aspx", ".ascx", ".master", ".ashx",
		".asmx", ".asax", ".editorconfig":
		return true
	}
	return false
}

func excludedDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".vs", "bin", "obj", "packages", "node_modules", ".onebyone":
		return true
	}
	return false
}

func canonicalRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("ワークスペースがディレクトリではありません")
	}
	return absolute, nil
}

func insideRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

func safePath(root, base, name string) (string, error) {
	name = filepath.FromSlash(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if name == "" || strings.ContainsAny(name, "\x00\r\n") {
		return "", fmt.Errorf("空または不正なファイルパスです")
	}
	// Reject drive-qualified paths on hosts that do not recognize Windows
	// volumes, and NTFS alternate data streams on every host.
	withoutVolume := strings.TrimPrefix(name, filepath.VolumeName(name))
	if strings.Contains(withoutVolume, ":") {
		return "", fmt.Errorf("不正なファイルパスです: %s", name)
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, name)
	}
	path = filepath.Clean(path)
	if !insideRoot(root, path) {
		return "", fmt.Errorf("ワークスペース外のファイルは対象にできません: %s", name)
	}
	rel, _ := filepath.Rel(root, path)
	current := root
	if rel == "." {
		return root, nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if excludedDirectory(part) {
			return "", fmt.Errorf("生成物・管理ディレクトリは対象にできません: %s", name)
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("シンボリックリンクは対象にできません: %s", name)
		}
	}
	return path, nil
}

func relative(root, path string) string {
	rel, _ := filepath.Rel(root, path)
	return filepath.ToSlash(rel)
}

func readText(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return "", fmt.Errorf("通常ファイルかつ8 MiB以下である必要があります: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	document, err := sourceencoding.Decode(data)
	if err != nil {
		return "", err
	}
	return document.Text, nil
}
