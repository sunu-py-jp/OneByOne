package solution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"onebyone/internal/model"
	"onebyone/internal/processutil"
	"onebyone/internal/sourceencoding"
)

// Build runs the selected solution using VS2022 MSBuild on Windows. A compiler
// failure is returned as a completed, unsuccessful build; inability to launch,
// cancellation, or a timeout is returned as an error with any collected log.
func Build(ctx context.Context, root string, settings model.SolutionSettings) (result model.SolutionBuild, buildErr error) {
	result = model.SolutionBuild{StartedAt: time.Now().UTC().Format(time.RFC3339Nano), ExitCode: -1, Diagnostics: []model.SourceDiagnostic{}}
	defer func() {
		if result.FinishedAt == "" {
			result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if buildErr != nil && result.Output == "" {
			result.Output = buildErr.Error()
		}
	}()
	if runtime.GOOS != "windows" {
		return result, fmt.Errorf("VS2022ソリューションのビルドにはWindowsとVisual Studio 2022またはBuild Tools 2022が必要です")
	}
	if _, err := buildArguments(root, settings); err != nil {
		return result, err
	}
	executable, err := findMSBuild(ctx, settings.MSBuildPath)
	if err != nil {
		return result, err
	}
	return runBuild(ctx, root, settings, executable)
}

var propertyValue = regexp.MustCompile(`^[\pL\pN][\pL\pN _().-]{0,127}$`)

func buildArguments(root string, settings model.SolutionSettings) ([]string, error) {
	base, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	path, err := safePath(base, base, settings.Path)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(path), ".sln") {
		return nil, fmt.Errorf("ビルド対象には.slnを指定してください")
	}
	configuration, platform := strings.TrimSpace(settings.Configuration), strings.TrimSpace(settings.Platform)
	if configuration == "" {
		configuration = "Debug"
	}
	if platform == "" {
		platform = "Any CPU"
	}
	if !propertyValue.MatchString(configuration) || !propertyValue.MatchString(platform) {
		return nil, fmt.Errorf("構成・プラットフォームには文字、数字、空白、_ . ( ) - のみ指定できます")
	}
	if settings.BuildTimeoutSeconds < 0 || settings.BuildTimeoutSeconds > 86400 {
		return nil, fmt.Errorf("ビルド制限時間は1〜86400秒で指定してください")
	}
	return []string{path, "/t:Rebuild", "/restore", "/p:RestorePackagesConfig=true", "/p:Configuration=" + configuration,
		"/p:Platform=" + platform, "/p:GenerateFullPaths=true", "/nologo", "/nr:false", "/m:1", "/v:minimal", "/clp:ForceConsoleColor=false;Summary"}, nil
}

func runBuild(ctx context.Context, root string, settings model.SolutionSettings, executable string) (model.SolutionBuild, error) {
	result := model.SolutionBuild{StartedAt: time.Now().UTC().Format(time.RFC3339Nano), ExitCode: -1, Diagnostics: []model.SourceDiagnostic{}}
	args, err := buildArguments(root, settings)
	if err != nil {
		return result, err
	}
	seconds := settings.BuildTimeoutSeconds
	if seconds == 0 {
		seconds = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	base, _ := canonicalRoot(root)
	capture := &buildCapture{root: base, solution: args[0], seen: map[string]bool{}}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = filepath.Dir(args[0])
	cmd.Env = englishEnvironment(os.Environ())
	cmd.Stdout, cmd.Stderr = capture, capture
	cmd.WaitDelay = 3 * time.Second
	configureProcess(cmd)
	err = cmd.Run()
	capture.finish()
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	result.Output = capture.String()
	result.Diagnostics = capture.diagnostics
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		return result, fmt.Errorf("ソリューションのビルドが中断されました: %w", ctx.Err())
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return result, nil
		}
		return result, fmt.Errorf("MSBuildを実行できません: %w", err)
	}
	result.Success = len(result.Diagnostics) == 0
	return result, nil
}

func englishEnvironment(env []string) []string {
	result := make([]string, 0, len(env)+2)
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		if !strings.EqualFold(key, "VSLANG") && !strings.EqualFold(key, "DOTNET_CLI_UI_LANGUAGE") {
			result = append(result, value)
		}
	}
	return append(result, "VSLANG=1033", "DOTNET_CLI_UI_LANGUAGE=en-US")
}

func findMSBuild(ctx context.Context, explicit string) (string, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		if !filepath.IsAbs(explicit) || !strings.EqualFold(filepath.Base(explicit), "MSBuild.exe") {
			return "", fmt.Errorf("MSBuild.exeの絶対パスを指定してください")
		}
		info, err := os.Stat(explicit)
		if err != nil {
			return "", fmt.Errorf("MSBuild.exeが見つかりません: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("MSBuild.exeのファイルを指定してください")
		}
		return explicit, nil
	}
	installerRoot := os.Getenv("ProgramFiles(x86)")
	if installerRoot == "" {
		installerRoot = os.Getenv("ProgramFiles")
	}
	if installerRoot == "" {
		return "", fmt.Errorf("Visual Studio Installerが見つかりません。MSBuild.exeの絶対パスを設定してください")
	}
	vswhere := filepath.Join(installerRoot, "Microsoft Visual Studio", "Installer", "vswhere.exe")
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Version 17 is VS2022. -products * includes the standalone Build Tools.
	cmd := exec.CommandContext(ctx, vswhere, "-latest", "-products", "*", "-version", "[17.0,18.0)", "-requires", "Microsoft.Component.MSBuild", "-find", `MSBuild\**\Bin\MSBuild.exe`, "-utf8")
	processutil.HideWindow(cmd)
	cmd.WaitDelay = time.Second
	var output bytes.Buffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Visual Studio 2022のMSBuildを検索できません。Visual Studio InstallerまたはMSBuild.exeの設定を確認してください: %w", err)
	}
	for _, line := range strings.Split(output.String(), "\n") {
		path := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Base(path), "MSBuild.exe") {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", fmt.Errorf("Visual Studio 2022 / Build Tools 2022のMSBuildが見つかりません。.NETデスクトップ開発ワークロードをインストールするかMSBuild.exeを指定してください")
}

const maxBuildOutput = 2 << 20
const maxDiagnosticLine = 256 << 10
const maxBuildDiagnostics = 10000

type buildCapture struct {
	mu                  sync.Mutex
	output              bytes.Buffer
	line                []byte
	dropLine            bool
	truncated           bool
	diagnosticTruncated bool
	diagnosticBytes     int
	root, solution      string
	diagnostics         []model.SourceDiagnostic
	seen                map[string]bool
}

func (b *buildCapture) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	remaining := maxBuildOutput - b.output.Len()
	if remaining < len(data) {
		b.truncated = true
	}
	if remaining > 0 {
		b.output.Write(data[:min(len(data), remaining)])
	}
	// Parse all output even after the display log has reached its limit.
	// Each line and the number of diagnostics are independently bounded.
	for _, char := range data {
		if char == '\n' {
			if !b.dropLine {
				b.consumeLine()
			}
			b.line, b.dropLine = b.line[:0], false
		} else if !b.dropLine {
			if len(b.line) < maxDiagnosticLine {
				b.line = append(b.line, char)
			} else {
				b.dropLine, b.diagnosticTruncated = true, true
			}
		}
	}
	return n, nil
}

func (b *buildCapture) finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.line) > 0 && !b.dropLine {
		b.consumeLine()
	}
	b.line = nil
	if b.diagnostics == nil {
		b.diagnostics = []model.SourceDiagnostic{}
	}
}

func (b *buildCapture) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := decodeOutput(b.output.Bytes())
	if b.truncated {
		value += "\n[ビルドログは2 MiBを超えたため表示を省略しました。診断の収集は継続しています。]\n"
	}
	if b.diagnosticTruncated {
		value += "\n[診断の件数または行長が上限を超えました。]\n"
	}
	return value
}

var compilerDiagnostic = regexp.MustCompile(`(?i)^(?:\s*\d+>)?\s*(.*?)\s*:\s*(?:fatal\s+)?error\s+([A-Z]+\d+)\s*:\s*(.*)$`)
var diagnosticPosition = regexp.MustCompile(`^(.*)\((\d+)(?:,\d+){0,3}\)$`)
var diagnosticProject = regexp.MustCompile(`\s+\[([^\[\]\r\n]+\.(?:vbproj|csproj|sln)(?:\:\:[^\[\]]*)?)\]\s*$`)

func (b *buildCapture) consumeLine() {
	line := strings.TrimSpace(decodeOutput(b.line))
	match := compilerDiagnostic.FindStringSubmatch(line)
	if match == nil {
		return
	}
	location, message := strings.TrimSpace(match[1]), strings.TrimSpace(match[3])
	project := b.solution
	if suffix := diagnosticProject.FindStringSubmatch(message); suffix != nil {
		project = strings.SplitN(suffix[1], "::", 2)[0]
		message = strings.TrimSpace(message[:len(message)-len(suffix[0])])
	}
	lineNumber := 0
	if position := diagnosticPosition.FindStringSubmatch(location); position != nil {
		location = position[1]
		lineNumber, _ = strconv.Atoi(position[2])
	}
	file := b.diagnosticPath(location, project)
	if file == "" {
		file = b.diagnosticPath(project, b.solution)
	}
	if file == "" {
		file = relative(b.root, b.solution)
	}
	diagnostic := model.SourceDiagnostic{File: file, Line: lineNumber, Code: strings.ToUpper(match[2]), Message: message}
	key := fmt.Sprintf("%s\x00%d\x00%s\x00%s", file, lineNumber, diagnostic.Code, message)
	if b.seen[key] {
		return
	}
	if len(b.diagnostics) >= maxBuildDiagnostics || b.diagnosticBytes+len(key) > 4<<20 {
		b.diagnosticTruncated = true
		return
	}
	b.seen[key] = true
	b.diagnosticBytes += len(key)
	b.diagnostics = append(b.diagnostics, diagnostic)
}

func (b *buildCapture) diagnosticPath(value, project string) string {
	value = filepath.FromSlash(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
	extension := strings.ToLower(filepath.Ext(value))
	if extension != ".vb" && extension != ".vbproj" && extension != ".csproj" && extension != ".sln" {
		return ""
	}
	if !filepath.IsAbs(value) {
		project = filepath.FromSlash(strings.ReplaceAll(project, "\\", "/"))
		if !filepath.IsAbs(project) {
			project = filepath.Join(filepath.Dir(b.solution), project)
		}
		candidate := filepath.Join(filepath.Dir(project), value)
		if _, err := os.Stat(candidate); err == nil {
			value = candidate
		} else {
			value = filepath.Join(filepath.Dir(b.solution), value)
		}
	}
	value = filepath.Clean(value)
	if !insideRoot(b.root, value) {
		return ""
	}
	return relative(b.root, value)
}

func decodeOutput(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	if decoded, err := sourceencoding.DecodeFragment(data, sourceencoding.ShiftJIS); err == nil {
		return decoded
	}
	return strings.ToValidUTF8(string(data), "�")
}
