package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"onebyone/internal/engine"
	"onebyone/internal/model"
)

type App struct {
	ctx     context.Context
	service *engine.Service
}

func NewApp() *App {
	path := os.Getenv("ONEBYONE_CONFIG")
	if path == "" {
		dir, e := engine.DefaultLocalDirectory()
		if e != nil {
			dir = "."
		}
		path = filepath.Join(dir, "app-settings.json")
	}
	return &App{service: engine.New(path)}
}
func (a *App) startup(ctx context.Context) { a.ctx = ctx }
func (a *App) beforeClose(ctx context.Context) bool {
	if !a.service.Snapshot().Running {
		return false
	}
	a.service.Stop()
	go func() { a.service.Wait(); runtime.Quit(ctx) }()
	return true
}
func (a *App) shutdown(ctx context.Context)   { a.service.Close() }
func (a *App) GetState() (model.State, error) { return a.service.GetState() }
func (a *App) CheckGitInstallation() model.GitInstallation {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return engine.CheckGitInstallation(ctx)
}
func (a *App) OpenGitInstallGuide() {
	runtime.BrowserOpenURL(a.ctx, "https://git-scm.com/install/")
}
func (a *App) CreateWorkspace(name, root string) (model.State, error) {
	return a.service.CreateWorkspace(name, root)
}
func (a *App) CreateDemoWorkspace(name, root string) (model.State, error) {
	return a.service.CreateDemoWorkspace(name, root)
}
func (a *App) ValidateTargetFolder(root string) (string, error) {
	return a.service.ValidateTargetFolder(root)
}
func (a *App) CreateDemoProject(parent string) (string, error) {
	return a.service.CreateDemoProject(parent)
}
func (a *App) ChangeTargetFolder(root string) (model.State, error) {
	return a.service.ChangeTargetFolder(root)
}
func (a *App) ListTargetFiles(workspaceID, root string) (model.TargetFileList, error) {
	return a.service.ListTargetFiles(workspaceID, root)
}
func (a *App) ReadTargetFile(workspaceID, root, file string) (model.TargetFileContent, error) {
	return a.service.ReadTargetFile(workspaceID, root, file)
}
func (a *App) ReadExecutionFile(workspaceID, root, file string) (model.TargetFileContent, error) {
	return a.service.ReadExecutionFile(workspaceID, root, file)
}
func (a *App) SelectWorkspace(id string) (model.State, error) {
	return a.service.SelectWorkspace(id)
}
func (a *App) RenameWorkspace(name string) (model.State, error) {
	return a.service.RenameWorkspace(name)
}
func (a *App) DuplicateWorkspace(name string) (model.State, error) {
	return a.service.DuplicateWorkspace(name)
}
func (a *App) DeleteWorkspace(id string) (model.State, error) {
	return a.service.DeleteWorkspace(id)
}
func (a *App) DeleteRule(id string) (model.State, error) {
	return a.service.DeleteRule(id)
}
func (a *App) SaveLLMConnection(c model.LLMConnection) (model.State, error) {
	return a.service.SaveLLMConnection(c)
}
func (a *App) DeleteLLMConnection(id string) (model.State, error) {
	return a.service.DeleteLLMConnection(id)
}
func (a *App) SelectLLMConnection(id string) (model.State, error) {
	return a.service.SelectLLMConnection(id)
}
func (a *App) ClearLLMConnectionCredential(id string) (model.State, error) {
	return a.service.ClearLLMConnectionCredential(id)
}
func (a *App) TestLLMConnection(id string) (string, error) {
	return a.service.TestLLMConnection(id)
}
func (a *App) SignInLLMConnection(id string) (model.State, error) {
	return a.service.SignInLLMConnection(id)
}
func (a *App) CancelLLMSignIn() { a.service.CancelLLMSignIn() }
func (a *App) SignOutLLMConnection(id string) (model.State, error) {
	return a.service.SignOutLLMConnection(id)
}
func (a *App) SaveConfig(c model.Config) (model.State, error) { return a.service.SaveConfig(c) }
func (a *App) Scan() (model.State, error)                     { return a.service.Scan() }
func (a *App) Start(limit int) error                          { return a.service.Start(limit) }
func (a *App) Stop()                                          { a.service.Stop() }
func (a *App) RetryTasks(files []string) (model.State, error) { return a.service.RetryTasks(files) }
func (a *App) DiscardFileChanges(file string) (model.State, error) {
	return a.service.DiscardFileChanges(file)
}
func (a *App) GetResultPublicationPreview() (model.ResultPublicationPreview, error) {
	return a.service.GetResultPublicationPreview()
}
func (a *App) GetResultPublicationFileDiff(workspaceID, revision, file string) (string, error) {
	return a.service.GetResultPublicationFileDiff(workspaceID, revision, file)
}
func (a *App) PublishResults(req model.PublishResultsRequest) (model.ResultPublication, error) {
	return a.service.PublishResults(req)
}
func (a *App) SetTaskSelection(files []string) (model.State, error) {
	return a.service.SetTaskSelection(files)
}
func (a *App) GetFileDetail(file string, attempt int) (model.FileDetail, error) {
	return a.service.GetFileDetail(file, attempt)
}
func (a *App) GetExecutionRun(id string) (model.ExecutionRunResult, error) {
	return a.service.GetExecutionRun(id)
}
func (a *App) GetExecutionFileDetail(id, file string, attempt int) (model.FileDetail, error) {
	return a.service.GetExecutionFileDetail(id, file, attempt)
}
func (a *App) ReadRule(id string) (model.Rule, error)             { return a.service.ReadRule(id) }
func (a *App) OpenRule(id string) (model.RuleEditor, error)       { return a.service.OpenRule(id) }
func (a *App) CloseRule() error                                   { return a.service.CloseRule() }
func (a *App) SaveRule(input model.RuleEdit) (model.State, error) { return a.service.SaveRule(input) }
func (a *App) SaveRules(edits []model.RuleEdit) (model.State, error) {
	return a.service.SaveRules(edits)
}
func (a *App) CreateRule(input model.RuleEdit) (model.State, error) {
	return a.service.CreateRule(input)
}
func (a *App) ChooseRulePackage() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "ルールパッケージを取り込む",
		Filters: []runtime.FileFilter{{DisplayName: "OneByOne rule package", Pattern: "*.oborules"}},
	})
}
func (a *App) ImportRulePackage(path, mode string) (model.State, error) {
	return a.service.ImportRulePackage(path, mode)
}
func (a *App) ExportRulePackage() (string, error) {
	st := a.service.Snapshot()
	name := st.Config.RulePackageName
	if name == "" {
		name = "rules.oborules"
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title: "ルールパッケージを書き出す", DefaultFilename: filepath.Base(name),
		Filters:              []runtime.FileFilter{{DisplayName: "OneByOne rule package", Pattern: "*.oborules"}},
		CanCreateDirectories: true,
	})
	if err != nil || path == "" {
		return "", err
	}
	if filepath.Ext(path) == "" {
		path += ".oborules"
	}
	return a.service.ExportRulePackage(path)
}
func (a *App) ExportReport() (string, error) {
	return a.exportReport("")
}
func (a *App) ExportExecutionReport(id string) (string, error) {
	return a.exportReport(id)
}
func (a *App) exportReport(executionID string) (string, error) {
	st := a.service.Snapshot()
	defaultDir := filepath.Dir(st.Config.QueuePath)
	if info, err := os.Stat(defaultDir); err != nil || !info.IsDir() {
		defaultDir = ""
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title: "結果を出力", DefaultDirectory: defaultDir,
		DefaultFilename:      "report-" + time.Now().Format("20060102-150405") + ".json",
		Filters:              []runtime.FileFilter{{DisplayName: "結果レポート (JSON)", Pattern: "*.json"}},
		CanCreateDirectories: true,
	})
	if err != nil || path == "" {
		return "", err
	}
	if filepath.Ext(path) == "" {
		path += ".json"
	}
	if executionID != "" {
		return a.service.ExportExecutionReport(executionID, path)
	}
	return a.service.ExportReport(path)
}
func (a *App) TestConnection() (string, error) { return a.service.TestConnection() }
func (a *App) ChooseDirectory(kind string) (string, error) {
	st := a.service.Snapshot()
	title := "フォルダを選択"
	defaultDir := ""
	switch kind {
	case "root":
		title = "対象ソースのフォルダ"
		defaultDir = st.Config.Root
	case "demo":
		title = "デモ保存先のディレクトリを選択"
	case "rules":
		title = "rules フォルダ"
		defaultDir = st.Config.RulesPath
	default:
		return "", fmt.Errorf("未対応のフォルダ選択です: %s", kind)
	}
	if _, e := os.Stat(defaultDir); e != nil {
		defaultDir = ""
	}
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: title, DefaultDirectory: defaultDir, CanCreateDirectories: kind == "demo"})
}

func (a *App) ChooseLegacy() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: "旧シンボルのパターン一覧", Filters: []runtime.FileFilter{{DisplayName: "Pattern files", Pattern: "*.txt;*.pattern"}}})
}
func (a *App) OpenWorktree() error {
	path := a.service.Snapshot().Worktree
	if path == "" {
		return fmt.Errorf("まだ作業コピーがありません")
	}
	return openFolder(path)
}
