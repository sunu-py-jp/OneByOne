package engine

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"onebyone/internal/model"
)

// Keep unreadable settings visible without adopting any of their configuration.
// Only its display name and protective path reservations are retained. This is
// not a compatibility conversion: selecting the workspace still requires the
// complete current format to validate.
func (s *Service) unreadableWorkspace(id string, cause error) workspaceRecord {
	w := model.Workspace{ID: id, Name: "読み込めないワークスペース (" + id[:8] + ")"}
	queue := ""
	if path, err := s.workspacePath(id, "setting.json"); err == nil {
		if data, err := readLocalJSON(path); err == nil {
			var label struct {
				Name   string `json:"name"`
				Config struct {
					Root      string `json:"root"`
					QueuePath string `json:"queuePath"`
				} `json:"config"`
			}
			if json.Unmarshal(data, &label) == nil {
				if strings.TrimSpace(label.Name) != "" {
					if name, err := cleanWorkspaceName(label.Name); err == nil {
						w.Name = name
					}
				}
				// Do not move a directory or reuse an external queue merely because
				// its owning workspace now has an unsupported setting. These paths
				// are used only for overlap protection, never to open the workspace.
				w.Root = workspaceReservedPath(label.Config.Root)
				queue = workspaceReservedPath(label.Config.QueuePath)
			}
		}
	}
	return workspaceRecord{Workspace: w, QueuePath: queue, LoadError: fmt.Errorf("ワークスペース「%s」の setting.json を読み込めません。この設定は使用できませんが、他のワークスペースは利用できます: %w", w.Name, cause)}
}

func workspaceReservedPath(path string) string {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return ""
	}
	return canonicalAlias(path)
}

// An unreadable saved selection must not prevent starting the app or choosing
// another workspace after deletion. Return empty when none can be opened.
func availableWorkspaceID(items []workspaceRecord, preferred string) string {
	first := ""
	for _, item := range items {
		if item.LoadError != nil {
			continue
		}
		if item.ID == preferred {
			return item.ID
		}
		if first == "" {
			first = item.ID
		}
	}
	return first
}

func validateWorkspaceQueue(items []workspaceRecord, id, queue string) error {
	if queue == "" {
		return nil
	}
	for _, w := range items {
		if w.ID != id && sameRoot(queue, w.QueuePath) {
			return fmt.Errorf("実行データの保存先がワークスペース「%s」と重複しています。ワークスペースを作り直してください", w.Name)
		}
	}
	return nil
}
