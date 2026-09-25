package engine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"onebyone/internal/model"
)

const targetFileListLimit = 50000
const targetFileContentLimit = 1 << 20

// targetFileRoot binds asynchronous reads to the workspace and folder that
// requested them. Browsing never depends on the queue, rules or Git cleanliness.
func (s *Service) targetFileRoot(workspaceID, root string) (string, error) {
	s.mu.Lock()
	id, selectedRoot, closed := s.state.ActiveWorkspaceID, s.state.Config.Root, s.closed
	s.mu.Unlock()
	if closed || workspaceID == "" || workspaceID != id || root == "" || !sameRoot(root, selectedRoot) {
		return "", fmt.Errorf("ワークスペースまたは対象フォルダが変更されています")
	}
	selectedRoot, err := settingsRoot(selectedRoot)
	if err != nil {
		return "", err
	}
	requestedRoot, err := settingsRoot(root)
	if err != nil {
		return "", err
	}
	a, err := os.Stat(selectedRoot)
	if err != nil {
		return "", err
	}
	b, err := os.Stat(requestedRoot)
	if err != nil || !os.SameFile(a, b) {
		return "", fmt.Errorf("対象フォルダが一致しません")
	}
	return selectedRoot, nil
}

func openTargetFileRoot(root string) (*os.Root, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("対象フォルダを開けません")
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	opened, err := handle.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		handle.Close()
		return nil, fmt.Errorf("対象フォルダが変更されています")
	}
	return handle, nil
}

func targetFileInfo(root *os.Root, file string) (os.FileInfo, error) {
	if err := validatePreviewFilePath(file); err != nil {
		return nil, err
	}
	parts := strings.Split(file, "/")
	var info os.FileInfo
	for i, part := range parts {
		if strings.EqualFold(part, ".git") {
			return nil, fmt.Errorf("Git管理ファイルは表示できません")
		}
		var err error
		info, err = root.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) {
			return nil, fmt.Errorf("シンボリックリンクや特殊ファイルは表示できません")
		}
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("通常のファイルではありません")
	}
	return info, nil
}

func validatePreviewFilePath(file string) error {
	if file == "" || file != path.Clean(file) || strings.ContainsAny(file, "\\\x00") || !filepath.IsLocal(filepath.FromSlash(file)) {
		return fmt.Errorf("対象ファイルのパスが不正です")
	}
	for _, part := range strings.Split(file, "/") {
		if strings.EqualFold(part, ".git") {
			return fmt.Errorf("Git管理ファイルは表示できません")
		}
	}
	return nil
}

// ListTargetFiles lists the original source, including untracked files, while
// respecting Git ignores. It does not extract migration targets or create files.
func (s *Service) ListTargetFiles(workspaceID, root string) (model.TargetFileList, error) {
	result := model.TargetFileList{WorkspaceID: workspaceID, Root: root, Files: []model.TargetFile{}, Limit: targetFileListLimit}
	canonical, err := s.targetFileRoot(workspaceID, root)
	if err != nil {
		return result, err
	}
	result.Root = canonical
	result.Files, result.Truncated, err = listTargetFiles(canonical, targetFileListLimit)
	if err != nil {
		return result, err
	}
	if _, err = s.targetFileRoot(workspaceID, canonical); err != nil {
		return model.TargetFileList{}, err
	}
	return result, nil
}

func splitTargetFileNames(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return 0, nil, fmt.Errorf("ファイル一覧が不完全です")
	}
	return 0, nil, nil
}

func listTargetFiles(root string, limit int) ([]model.TargetFile, bool, error) {
	files := []model.TargetFile{}
	handle, err := openTargetFileRoot(root)
	if err != nil {
		return files, false, err
	}
	defer handle.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	installation := CheckGitInstallation(ctx)
	if !installation.Available {
		return files, false, fmt.Errorf("%s", installation.Message)
	}
	cmd, err := runtimeCommand(ctx, installation.Path, "--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false", "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", ".")
	if err != nil {
		return files, false, err
	}
	cmd.Dir = root
	cmd.WaitDelay = 3 * time.Second
	stderr := &limitedBuffer{max: 8192}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return files, false, err
	}
	if err = cmd.Start(); err != nil {
		return files, false, fmt.Errorf("対象ファイルの一覧を取得できません: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Split(splitTargetFileNames)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	seen := make(map[string]bool)
	truncated := false
	for scanner.Scan() {
		file := scanner.Text()
		if seen[file] || !utf8.ValidString(file) {
			continue
		}
		info, statErr := targetFileInfo(handle, file)
		if statErr != nil {
			continue // Deleted index entries, links and submodules are not files to preview.
		}
		if len(files) >= limit {
			truncated = true
			cancel()
			break
		}
		seen[file] = true
		files = append(files, model.TargetFile{File: file, Size: info.Size()})
	}
	if scanner.Err() != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if scanner.Err() != nil {
		return files, false, fmt.Errorf("対象ファイルの一覧を読み込めません: %w", scanner.Err())
	}
	if !truncated && (waitErr != nil || ctx.Err() != nil) {
		return files, false, fmt.Errorf("対象ファイルの一覧を取得できません: %v %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].File < files[j].File })
	return files, truncated, nil
}

// ReadTargetFile returns bounded, read-only UTF-8 content from the source root.
// os.Root also prevents path escapes if an ancestor changes during the read.
func (s *Service) ReadTargetFile(workspaceID, root, file string) (model.TargetFileContent, error) {
	result := model.TargetFileContent{WorkspaceID: workspaceID, Root: root, File: file}
	canonical, err := s.targetFileRoot(workspaceID, root)
	if err != nil {
		return result, err
	}
	result.Root = canonical
	result, err = readPreviewFile(result, canonical, file)
	if err != nil {
		return result, err
	}
	if _, err = s.targetFileRoot(workspaceID, canonical); err != nil {
		return model.TargetFileContent{}, err
	}
	return result, nil
}

// readPreviewFile shares the bounded reader, but never resolves a caller's root.
// Public entry points must authorize the workspace and select the root first.
func readPreviewFile(result model.TargetFileContent, root, file string) (model.TargetFileContent, error) {
	handle, err := openTargetFileRoot(root)
	if err != nil {
		return result, err
	}
	defer handle.Close()
	info, err := targetFileInfo(handle, file)
	if err != nil {
		return result, fmt.Errorf("対象ファイルを開けません: %w", err)
	}
	result.Size = info.Size()
	if result.Size > targetFileContentLimit {
		result.UnavailableReason = "1 MiBを超えるファイルはプレビューできません"
	} else {
		opened, err := handle.Open(filepath.FromSlash(file))
		if err != nil {
			return result, err
		}
		defer opened.Close()
		current, err := opened.Stat()
		if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
			return result, fmt.Errorf("対象ファイルが変更されています。再度選択してください")
		}
		data, err := io.ReadAll(io.LimitReader(opened, targetFileContentLimit+1))
		if err != nil {
			return result, fmt.Errorf("対象ファイルを読み込めません: %w", err)
		}
		result.Size = int64(len(data))
		switch {
		case len(data) > targetFileContentLimit:
			result.UnavailableReason = "1 MiBを超えるファイルはプレビューできません"
		case bytes.IndexByte(data, 0) >= 0:
			result.UnavailableReason = "バイナリファイルはプレビューできません"
		case !utf8.Valid(data):
			result.UnavailableReason = "UTF-8以外の文字コードのファイルはプレビューできません"
		default:
			result.Content = strings.TrimPrefix(string(data), "\ufeff")
		}
	}
	return result, nil
}
