package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"onebyone/internal/model"
	"onebyone/internal/store"
)

func lockSharedQueue(cfg model.Config) (func(), error) {
	if err := prepareSharedOutput(cfg); err != nil {
		return nil, err
	}
	lease, owner, err := store.AcquireWorkspace(cfg.QueuePath+".lock", currentWorkspaceOwner())
	if err != nil {
		return nil, err
	}
	if owner != nil {
		return nil, fmt.Errorf("別の実行がこのキューを使用中です")
	}
	return func() { _ = lease.Release() }, nil
}
func writeOutputJSON(cfg model.Config, path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeSharedArtifact(cfg, path, append(b, '\n'))
}
func writeOutputQueue(cfg model.Config, tasks []model.Task) error {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, task := range tasks {
		if err := enc.Encode(task); err != nil {
			return err
		}
	}
	return writeSharedArtifact(cfg, cfg.QueuePath, []byte(b.String()))
}
