package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"onebyone/internal/diagnostics"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

// ImportDiagnostics snapshots the supplied JSONL into a new workspace-owned
// version. Publishing setting.json is the sole commit point; a rejected import
// cannot alter the previous copy, queue, or historical attempts.
func (s *Service) ImportDiagnostics(path string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	w, cfg := s.diagnosticsWorkspace()
	if w.ID == "" || cfg.Root == "" {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを選択してください")
	}
	items, err := diagnostics.Read(path)
	if err != nil {
		return s.Snapshot(), err
	}
	name := filepath.Base(path)
	copyPath, err := s.writeDiagnosticsVersion(w.ID, name, items)
	if err != nil {
		return s.Snapshot(), err
	}
	cfg.DiagnosticsPath = copyPath
	cfg.DiagnosticsSourceFile = ""
	if absolute, err := filepath.Abs(path); err == nil {
		if rel, err := filepath.Rel(cfg.Root, canonicalAlias(absolute)); err == nil && rel != "." && filepath.IsLocal(rel) {
			// Retain only an exclusion name. The external input is never read
			// again after its immutable copy has been created.
			cfg.DiagnosticsSourceFile = filepath.ToSlash(rel)
		}
	}
	if err = s.writeWorkspaceSetting(w, cfg); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	s.state.Config = cfg
	s.state.Diagnostics = diagnosticsSummary(name, items)
	s.cat = nil
	s.setWorkspaceIssueLocked(w.ID, "diagnostics", nil, "", "")
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	s.refreshActiveWorkspaceDiagnostics()
	return s.Snapshot(), nil
}

// ClearDiagnostics releases the active reference. Older immutable copies stay
// available to history; no supplied input file is ever modified or deleted.
func (s *Service) ClearDiagnostics() (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	w, cfg := s.diagnosticsWorkspace()
	if w.ID == "" {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを選択してください")
	}
	cfg.DiagnosticsPath, cfg.DiagnosticsOnly = "", false
	cfg.DiagnosticsSourceFile = ""
	if err := s.writeWorkspaceSetting(w, cfg); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	s.state.Config = cfg
	s.state.Diagnostics = nil
	s.cat = nil
	s.setWorkspaceIssueLocked(w.ID, "diagnostics", nil, "", "")
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	s.refreshActiveWorkspaceDiagnostics()
	return s.Snapshot(), nil
}

func (s *Service) diagnosticsWorkspace() (model.Workspace, model.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := model.Workspace{ID: s.state.ActiveWorkspaceID, Root: s.state.Config.Root}
	for _, item := range s.workspaces {
		if item.ID == w.ID {
			w.Name = item.Name
			break
		}
	}
	return w, s.state.Config
}

func diagnosticsSummary(name string, items []model.SourceDiagnostic) *model.DiagnosticSummary {
	files := map[string]bool{}
	for _, item := range items {
		files[item.File] = true
	}
	return &model.DiagnosticSummary{Name: name, Count: len(items), FileCount: len(files)}
}

func readDiagnosticsSummary(path string) (*model.DiagnosticSummary, error) {
	if path == "" {
		return nil, nil
	}
	items, err := diagnostics.Read(path)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	var metadata struct {
		Name string `json:"name"`
	}
	// The optional label never controls a read or a write path.
	if err = decodeLocalJSON(filepath.Join(filepath.Dir(path), "metadata.json"), &metadata); err == nil && metadata.Name != "" {
		name = metadata.Name
	}
	return diagnosticsSummary(name, items), nil
}

func (s *Service) writeDiagnosticsVersion(id, name string, items []model.SourceDiagnostic) (string, error) {
	data, err := diagnostics.Encode(items)
	if err != nil {
		return "", err
	}
	version := uid()
	path, err := s.workspacePath(id, filepath.Join("diagnostics", version, "diagnostics.jsonl"))
	if err != nil {
		return "", err
	}
	if err = ensureSharedOutputDirectory(filepath.Dir(filepath.Dir(path)), 0700); err != nil {
		return "", err
	}
	// Never replace a previous import even in the unlikely event of an ID clash.
	if err = os.Mkdir(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err = store.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	if err = store.WriteJSON(filepath.Join(filepath.Dir(path), "metadata.json"), struct {
		Name string `json:"name"`
	}{Name: name}); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Service) copyWorkspaceDiagnostics(c model.Config, id string) (model.Config, error) {
	if c.DiagnosticsPath == "" {
		return c, nil
	}
	items, err := diagnostics.Read(c.DiagnosticsPath)
	if err != nil {
		return c, err
	}
	summary, err := readDiagnosticsSummary(c.DiagnosticsPath)
	if err != nil {
		return c, err
	}
	c.DiagnosticsPath, err = s.writeDiagnosticsVersion(id, summary.Name, items)
	return c, err
}

// Validate ownership without reading the import. A missing diagnostics file
// must not stop the application from listing or repairing other workspaces.
func (s *Service) validateWorkspaceDiagnosticsPath(id, path string) error {
	if path == "" {
		return nil
	}
	root, err := s.workspacePath(id, "diagnostics")
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if err != nil || !filepath.IsAbs(path) || len(parts) != 2 || !workspaceIDPattern.MatchString(parts[0]) || parts[1] != "diagnostics.jsonl" {
		return fmt.Errorf("診断JSONLはこのワークスペースへ取り込んだコピーを使用してください")
	}
	_, err = s.workspacePath(id, filepath.Join("diagnostics", rel))
	return err
}
