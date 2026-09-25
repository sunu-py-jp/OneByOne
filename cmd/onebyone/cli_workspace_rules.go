package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"onebyone/internal/engine"
	"onebyone/internal/model"
)

// These command adapters leave validation, workspace ownership, package
// transactions, and rule leases with the same service used by the desktop UI.
// Parse every argument before calling it so rejected invocations cannot mutate.
func parseWorkspaceRuleFlags(name string, args []string, configure func(*flag.FlagSet)) error {
	f := flag.NewFlagSet("onebyone "+name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	configure(f)
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("%s に位置引数は指定できません: %s", name, strings.Join(f.Args(), " "))
	}
	return nil
}

func requireWorkspaceRuleValue(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("--%s を指定してください", name)
	}
	return nil
}

func executeWorkspaceCommand(ctx context.Context, s *engine.Service, args []string, _ io.Reader) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New("workspace の操作を指定してください: list / create / select / rename / duplicate / delete / target / validate")
	}
	command, rest := args[0], args[1:]
	var name, root, id string
	var yes bool
	var configure func(*flag.FlagSet)
	switch command {
	case "list":
		configure = func(*flag.FlagSet) {}
	case "create":
		configure = func(f *flag.FlagSet) {
			f.StringVar(&name, "name", "", "ワークスペース名")
			f.StringVar(&root, "root", "", "対象フォルダ")
		}
	case "select":
		configure = func(f *flag.FlagSet) { f.StringVar(&id, "id", "", "ワークスペースID") }
	case "rename", "duplicate":
		configure = func(f *flag.FlagSet) { f.StringVar(&name, "name", "", "ワークスペース名") }
	case "delete":
		configure = func(f *flag.FlagSet) {
			f.StringVar(&id, "id", "", "削除するワークスペースID")
			f.BoolVar(&yes, "yes", false, "削除を確認")
		}
	case "target", "validate":
		configure = func(f *flag.FlagSet) { f.StringVar(&root, "root", "", "対象フォルダ") }
	default:
		return nil, fmt.Errorf("不明な workspace 操作 %q", command)
	}
	if err := parseWorkspaceRuleFlags("workspace "+command, rest, configure); err != nil {
		return nil, err
	}
	switch command {
	case "create", "rename", "duplicate":
		if err := requireWorkspaceRuleValue("name", name); err != nil {
			return nil, err
		}
	}
	switch command {
	case "create", "target", "validate":
		if err := requireWorkspaceRuleValue("root", root); err != nil {
			return nil, err
		}
	case "select", "delete":
		if err := requireWorkspaceRuleValue("id", id); err != nil {
			return nil, err
		}
	}
	if command == "delete" && !yes {
		return nil, errors.New("削除するワークスペースを確認し、--yes を指定してください")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if validatingCLIArguments(ctx) {
		return nil, nil
	}
	switch command {
	case "list":
		state := s.Snapshot()
		return struct {
			ActiveWorkspaceID string            `json:"activeWorkspaceId"`
			Workspaces        []model.Workspace `json:"workspaces"`
		}{state.ActiveWorkspaceID, state.Workspaces}, nil
	case "create":
		return s.CreateWorkspace(name, root)
	case "select":
		return s.SelectWorkspace(id)
	case "rename":
		return s.RenameWorkspace(name)
	case "duplicate":
		return s.DuplicateWorkspace(name)
	case "delete":
		if s.Snapshot().ActiveWorkspaceID != id {
			if _, err := s.SelectWorkspace(id); err != nil {
				return nil, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return s.DeleteWorkspace(id)
	case "target":
		return s.ChangeTargetFolder(root)
	case "validate":
		validated, err := s.ValidateTargetFolder(root)
		if err != nil {
			return nil, err
		}
		return map[string]string{"root": validated}, nil
	}
	panic("validated workspace command was not handled")
}

func executeRulesCommand(ctx context.Context, s *engine.Service, args []string, in io.Reader) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New("rules の操作を指定してください: list / show / create / update / delete / import / export")
	}
	command, rest := args[0], args[1:]
	var id, input, output, mode string
	var yes bool
	var configure func(*flag.FlagSet)
	switch command {
	case "list":
		configure = func(*flag.FlagSet) {}
	case "show":
		configure = func(f *flag.FlagSet) { f.StringVar(&id, "id", "", "ルールID") }
	case "create", "update":
		configure = func(f *flag.FlagSet) {
			f.StringVar(&input, "input", "", "RuleEdit JSONファイル（- は標準入力）")
		}
	case "delete":
		configure = func(f *flag.FlagSet) {
			f.StringVar(&id, "id", "", "削除するルールID")
			f.BoolVar(&yes, "yes", false, "削除を確認")
		}
	case "import":
		configure = func(f *flag.FlagSet) {
			f.StringVar(&input, "input", "", ".oborules ファイル")
			f.StringVar(&mode, "mode", "", "replace または merge")
		}
	case "export":
		configure = func(f *flag.FlagSet) { f.StringVar(&output, "output", "", "保存先 .oborules ファイル") }
	default:
		return nil, fmt.Errorf("不明な rules 操作 %q", command)
	}
	if err := parseWorkspaceRuleFlags("rules "+command, rest, configure); err != nil {
		return nil, err
	}
	switch command {
	case "show", "delete":
		if err := requireWorkspaceRuleValue("id", id); err != nil {
			return nil, err
		}
	case "create", "update", "import":
		if err := requireWorkspaceRuleValue("input", input); err != nil {
			return nil, err
		}
	case "export":
		if err := requireWorkspaceRuleValue("output", output); err != nil {
			return nil, err
		}
	}
	if command == "delete" && !yes {
		return nil, errors.New("削除するルールを確認し、--yes を指定してください")
	}
	if command == "import" {
		if mode != "" && mode != "replace" && mode != "merge" {
			return nil, errors.New("--mode は replace または merge を指定してください")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if validatingCLIArguments(ctx) {
		return nil, nil
	}
	if command == "import" && mode == "" {
		state := s.Snapshot()
		if state.Config.RulesPath != "" || len(state.Rules) > 0 {
			return nil, errors.New("既存のルールがあります。--mode replace または --mode merge を指定してください")
		}
		mode = "replace"
	}
	switch command {
	case "list":
		return s.Snapshot().Rules, nil
	case "show":
		defer s.CloseRule()
		return s.OpenRule(id)
	case "create", "update":
		var edit model.RuleEdit
		if err := readCLIJSON(in, input, &edit); err != nil {
			return nil, err
		}
		if command == "update" && strings.TrimSpace(edit.ExpectedRevision) == "" {
			return nil, errors.New("ルール更新には expectedRevision が必要です。rules show --id で最新の内容と revision を確認してください")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		defer s.CloseRule()
		if command == "create" {
			return s.CreateRule(edit)
		}
		editor, err := s.OpenRule(edit.ID)
		if err != nil {
			return nil, err
		}
		if editor.ReadOnly {
			return nil, fmt.Errorf("このルールは閲覧専用です。他のユーザーが編集中です（%s / %s）", editor.LockOwner, editor.LockHost)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// The caller's revision is intentionally preserved. Replacing it with
		// the freshly opened revision would silently overwrite a concurrent edit.
		return s.SaveRule(edit)
	case "delete":
		return s.DeleteRule(id)
	case "import":
		return s.ImportRulePackage(input, mode)
	case "export":
		path, err := s.ExportRulePackage(output)
		if err != nil {
			return nil, err
		}
		return map[string]string{"path": path}, nil
	}
	panic("validated rules command was not handled")
}
