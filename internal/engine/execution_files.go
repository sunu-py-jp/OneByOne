package engine

import (
	"fmt"
	"path"
	"path/filepath"

	"onebyone/internal/model"
)

type executionPreviewSource struct {
	root       string
	worktree   string
	subfolder  string
	queue      string
	sourceRoot string
	sourceFile string
}

// executionPreviewRoot selects the same input directory as sourceDirLocked.
// The client supplies only the selected target root, never a worktree path.
func (s *Service) executionPreviewRoot(workspaceID, targetRoot, file string) (executionPreviewSource, error) {
	canonical, err := s.targetFileRoot(workspaceID, targetRoot)
	if err != nil {
		return executionPreviewSource{}, err
	}
	if err := validatePreviewFilePath(file); err != nil {
		return executionPreviewSource{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || workspaceID != s.state.ActiveWorkspaceID || !sameRoot(canonical, s.state.Config.Root) {
		return executionPreviewSource{}, fmt.Errorf("ワークスペースまたは対象フォルダが変更されています")
	}
	found := false
	for _, task := range s.state.Tasks {
		if task.File == file {
			found = true
			break
		}
	}
	if !found {
		return executionPreviewSource{}, fmt.Errorf("対象一覧にないファイルは表示できません")
	}
	source := executionPreviewSource{
		root: canonical, worktree: s.state.Worktree, subfolder: s.meta.SourceRelative,
		queue: s.state.Config.QueuePath, sourceRoot: canonical, sourceFile: file,
	}
	if source.worktree != "" {
		if !filepath.IsAbs(source.worktree) {
			return executionPreviewSource{}, fmt.Errorf("作業コピーのパスが不正です")
		}
		relative := filepath.ToSlash(source.subfolder)
		if relative == "" {
			relative = "."
		}
		if err := validatePreviewFilePath(relative); err != nil {
			return executionPreviewSource{}, fmt.Errorf("作業コピーの対象フォルダが不正です: %w", err)
		}
		// Open the worktree itself and walk every subfolder below it. Opening
		// the joined sourceDir directly could follow a replaced subfolder link.
		source.sourceRoot = source.worktree
		source.sourceFile = path.Join(relative, file)
	}
	return source, nil
}

// ReadExecutionFile previews the current input without creating a worktree,
// recovering attempts, changing Git state, or reading historical proposals.
func (s *Service) ReadExecutionFile(workspaceID, targetRoot, file string) (model.TargetFileContent, error) {
	source, err := s.executionPreviewRoot(workspaceID, targetRoot, file)
	if err != nil {
		return model.TargetFileContent{}, err
	}
	result, err := readPreviewFile(model.TargetFileContent{WorkspaceID: workspaceID, Root: source.root, File: file}, source.sourceRoot, source.sourceFile)
	if err != nil {
		return result, err
	}
	current, err := s.executionPreviewRoot(workspaceID, targetRoot, file)
	if err != nil {
		return model.TargetFileContent{}, err
	}
	if current != source {
		return model.TargetFileContent{}, fmt.Errorf("実行対象が変更されています。再読み込みしてください")
	}
	return result, nil
}
