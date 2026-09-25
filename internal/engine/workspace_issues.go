package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
)

type workspaceSessionError struct{ err error }

func (e *workspaceSessionError) Error() string {
	return fmt.Sprintf("セッション情報: %v", e.err)
}
func (e *workspaceSessionError) Unwrap() error { return e.err }

// Diagnostics are refreshed at explicit idle operations, never by Snapshot or
// by running-state polling. Keeping them outside state preserves other
// workspaces' errors when the active state is replaced during selection.
func (s *Service) setWorkspaceIssueLocked(id, source string, err error, page, file string) {
	if id == "" {
		return
	}
	if s.workspaceIssues == nil {
		s.workspaceIssues = map[string]map[string]model.WorkspaceIssue{}
	}
	if s.workspaceIssues[id] == nil {
		s.workspaceIssues[id] = map[string]model.WorkspaceIssue{}
	}
	previous := s.workspaceIssues[id][source]
	if err == nil {
		delete(s.workspaceIssues[id], source)
		if s.state.ActiveWorkspaceID == id && s.state.LastError == previous.Message {
			s.state.LastError = ""
		}
		return
	}
	issue := model.WorkspaceIssue{ID: source, Message: s.redactLocked(err.Error()), Page: page, File: file}
	if source == "target" {
		issue.Section = "target"
	}
	var sessionError *workspaceSessionError
	if errors.As(err, &sessionError) {
		issue.Page = "results"
	}
	var diagnostic *catalog.DiagnosticError
	if errors.As(err, &diagnostic) {
		issue.Page, issue.RuleID, issue.Section, issue.File = "rules", diagnostic.RuleID, diagnostic.Section, ""
	}
	s.workspaceIssues[id][source] = issue
}

func (s *Service) cachedWorkspaceIssuesLocked(id string) []model.WorkspaceIssue {
	var issues []model.WorkspaceIssue
	for _, issue := range s.workspaceIssues[id] {
		issues = append(issues, issue)
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].ID < issues[j].ID })
	return issues
}

func (s *Service) redactLocked(message string) string {
	secrets := []string{s.state.Config.Credential}
	for _, connection := range s.state.LLMConnections {
		secrets = append(secrets, connection.Credential)
	}
	for _, key := range []string{"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_AUTH_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		secrets = append(secrets, os.Getenv(key))
	}
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	if len(message) > 8192 {
		message = message[:8192] + "…"
	}
	return message
}

func passiveCatalog(c model.Config) (*catalog.Catalog, error) {
	if c.RulesPath == "" {
		return nil, nil
	}
	c.RGPath = "" // Viewing settings never executes a configured external program.
	return catalog.Load(context.Background(), c)
}

func (s *Service) updateTaskIssuesLocked(id string, tasks []model.Task) {
	if id == "" {
		return
	}
	for source := range s.workspaceIssues[id] {
		if strings.HasPrefix(source, "task:") {
			delete(s.workspaceIssues[id], source)
		}
	}
	for _, task := range tasks {
		failed, section := task.Status == "failed", ""
		if task.Status != "failed" && task.Status != "needs_human" {
			continue
		}
		if len(task.History) > 0 {
			latest := task.History[len(task.History)-1]
			failed = failed || latest.Outcome == "failed"
			for _, check := range latest.Checks {
				if check.Status == "failed" {
					failed, section = true, "checks"
				}
			}
		}
		if !failed {
			continue
		}
		message := task.Note
		if message == "" {
			message = "ファイルの処理に失敗しました"
		}
		source := "task:" + task.File
		s.setWorkspaceIssueLocked(id, source, errors.New(task.File+": "+message), "results", task.File)
		issue := s.workspaceIssues[id][source]
		issue.Section = section
		s.workspaceIssues[id][source] = issue
	}
}

// inspectWorkspaceDiagnostics is read-only, including for inactive workspaces.
// A temporary state validates restoration without acquiring locks or changing
// this service's selected workspace, tasks, or connection.
func (s *Service) inspectWorkspaceDiagnostics(id string, c model.Config, updateRules bool) {
	_, targetErr := s.ValidateTargetFolder(c.Root)
	probe := &Service{state: emptyState(c)}
	queueErr := probe.restoreWorkspaceQueue(c)
	cat, catalogErr := passiveCatalog(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setWorkspaceIssueLocked(id, "target", targetErr, "target", "")
	s.setWorkspaceIssueLocked(id, "queue", queueErr, "results", "")
	s.setWorkspaceIssueLocked(id, "catalog", catalogErr, "rules", "")
	if updateRules && id == s.state.ActiveWorkspaceID {
		s.cat = cat
		s.state.Rules = []model.Rule{}
		if cat != nil {
			s.state.Rules = cat.Rules
		}
		s.recountLocked()
	} else if queueErr == nil {
		s.updateTaskIssuesLocked(id, probe.state.Tasks)
	}
	s.publishWorkspacesLocked()
}

func (s *Service) refreshActiveWorkspaceDiagnostics() {
	s.mu.Lock()
	id, c := s.state.ActiveWorkspaceID, s.state.Config
	s.mu.Unlock()
	if id != "" {
		s.inspectWorkspaceDiagnostics(id, c, true)
	}
}
