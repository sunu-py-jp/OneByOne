// Package demopreset contains the offline, versioned demo project and its rules.
// It only returns fresh in-memory snapshots; callers own installation and Git.
package demopreset

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
)

const (
	ProjectName         = "onebyone-demo"
	ProjectFileCount    = 28
	TargetFileCount     = 23
	CommonRuleCount     = 3
	IndividualRuleCount = 10
)

// The all: prefix deliberately includes the project's .gitignore.
//
//go:embed all:project all:rulepack
var assets embed.FS

// ProjectFiles returns slash-separated paths relative to a new project root.
// Every call returns an independent map and byte slices.
func ProjectFiles() (map[string][]byte, error) {
	files, err := readFiles("project")
	if err != nil {
		return nil, err
	}
	if len(files) != ProjectFileCount {
		return nil, fmt.Errorf("デモプロジェクトのファイル数が不正です")
	}
	return files, nil
}

// Rules returns a portable rule package without LLM connections, credentials,
// target paths, result paths, execution limits or prices. It uses the same
// schema as imported packages.
func Rules() (*rulepack.Package, error) {
	files, err := readFiles("rulepack")
	if err != nil {
		return nil, err
	}
	settingsData, ok := files["settings.json"]
	if !ok {
		return nil, fmt.Errorf("デモルールの設定がありません")
	}
	delete(files, "settings.json")
	var settings rulepack.Settings
	decoder := json.NewDecoder(bytes.NewReader(settingsData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return nil, fmt.Errorf("デモルールの設定を読み込めません: %w", err)
	}
	common, individual := 0, 0
	for name, data := range files {
		if !strings.HasPrefix(name, "rules/") || !strings.HasSuffix(name, "/rule.json") {
			continue
		}
		definition, err := ruleformat.Decode(data)
		if err != nil {
			return nil, fmt.Errorf("デモルール %s を読み込めません: %w", name, err)
		}
		if strings.TrimSpace(definition.Pattern) == "" {
			common++
		} else {
			individual++
		}
	}
	if common != CommonRuleCount || individual != IndividualRuleCount {
		return nil, fmt.Errorf("デモルールの件数が不正です")
	}
	return &rulepack.Package{Settings: settings, Files: files}, nil
}

func readFiles(directory string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := fs.WalkDir(assets, directory, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := assets.ReadFile(name)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(name, directory+"/")] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("同梱デモを読み込めません: %w", err)
	}
	return files, nil
}
