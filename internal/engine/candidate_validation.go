package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

// Only the runner constructs this input. Source is the source subdirectory in
// its isolated worktree; Config.Root remains the user's original checkout.
type candidateValidationInput struct {
	Config    model.Config
	Catalog   *catalog.Catalog
	Source    string
	Worktree  string
	Relative  string
	Head      string
	Artifact  string
	File      string
	Before    []byte
	InputHash string
	// Journal must durably record CandidateHash before any candidate bytes are
	// written, so process-crash recovery can identify the interrupted write.
	Journal func(model.CandidateValidation) error
}

// validateCandidate never adopts a change. Both accepted and rejected proposals
// leave the isolated worktree at the same clean commit. Ordinary edit/check
// failures are returned to the LLM; infrastructure and isolation failures stop
// the runner because continuing could overwrite unknown state.
func validateCandidate(ctx context.Context, in candidateValidationInput, request model.CandidateRequest) (result model.CandidateValidation, resultErr error) {
	result = model.CandidateValidation{
		CandidateID: uid(), PlanRevision: request.PlanRevision,
		Checks: []model.Check{}, Diagnostics: []model.CandidateDiagnostic{}, RemainingItemIDs: []string{},
	}
	if in.Catalog == nil || in.Head == "" || in.Artifact == "" || in.InputHash == "" || digest(in.Before) != in.InputHash {
		return result, fmt.Errorf("候補検証の元データが一致しません")
	}
	if forbiddenContext(in.File) {
		return result, fmt.Errorf("設定・認証ファイルは候補検証の対象外です")
	}
	path, err := catalog.PathWithin(in.Source, in.File)
	if err != nil {
		return result, err
	}
	worktreePath, err := catalog.PathWithin(in.Worktree, in.Relative)
	if err != nil || !sameRoot(path, worktreePath) || sameRoot(path, filepath.Join(in.Config.Root, filepath.FromSlash(in.File))) {
		return result, fmt.Errorf("候補検証の対象が専用作業コピーと一致しません")
	}
	artifact := in.Artifact + ".candidate-" + result.CandidateID
	if candidatePathInside(in.Worktree, artifact) || candidatePathInside(in.Config.Root, artifact) {
		return result, fmt.Errorf("候補の検証記録は対象リポジトリと作業コピーの外に保存してください")
	}
	originalPath := filepath.Join(in.Config.Root, filepath.FromSlash(in.File))
	touched := false
	defer func() {
		if touched {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := rollback(cleanup, in.Worktree, in.Head); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("候補検証の巻き戻しに失敗しました: %w", err))
			} else if err := candidateBaseUnchanged(cleanup, in, path); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("候補検証後の元状態を確認できません: %w", err))
			}
		}
		if resultErr != nil {
			result.Passed = false
			result.Checks = append(result.Checks, model.Check{Name: "候補検証", Status: "failed", Detail: resultErr.Error()})
		}
		result.Diagnostics = candidateDiagnostics(result.Checks, filepath.Base(path))
		data, err := json.MarshalIndent(result, "", "  ")
		if err == nil {
			err = writeSharedArtifact(in.Config, artifact+".validation.json", append(data, '\n'), originalPath)
		}
		if err != nil {
			result.Passed = false
			resultErr = errors.Join(resultErr, fmt.Errorf("候補の検証記録を保存できません: %w", err))
		}
	}()
	if err = candidateBaseUnchanged(ctx, in, path); err != nil {
		return result, err
	}
	if request.BaseHash != in.InputHash {
		result.Checks = append(result.Checks, model.Check{Name: "編集元のハッシュ", Status: "failed", Detail: "baseHashが最初に渡された元ファイルのハッシュと一致しません。すべての変更は元ファイルを基準に指定してください"})
		return result, nil
	}
	original, err := decodeSource(in.Before)
	if err != nil {
		return result, err
	}
	afterText, err := applyEdits(original.text, request.Edits)
	if err != nil {
		result.Checks = append(result.Checks, model.Check{Name: "変更案の適用", Status: "failed", Detail: err.Error()})
		return result, nil
	}
	after := original.encode(afterText)
	if len(after) > in.Config.EffectiveMaxFileBytes() {
		result.Checks = append(result.Checks, model.Check{Name: "ファイルサイズ", Status: "failed", Detail: "修正後のファイルがサイズ上限を超えています"})
		return result, nil
	}
	result.CandidateHash = digest(after)
	result.EditRanges = exactEditLineRanges(original.text, afterText, request.Edits)
	if err = writeSharedArtifact(in.Config, artifact+".after", after, originalPath); err != nil {
		return result, err
	}
	if err = writeSharedArtifact(in.Config, in.Artifact+".after", after, originalPath); err != nil {
		return result, err
	}
	if in.Journal != nil {
		if err = in.Journal(result); err != nil {
			return result, err
		}
	}
	// Recheck after artifact/journal I/O, immediately before publication. A
	// conflicting pre-existing change never enters the rollback path.
	path, err = catalog.PathWithin(in.Source, in.File)
	if err != nil {
		return result, err
	}
	if err = candidateBaseUnchanged(ctx, in, path); err != nil {
		return result, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return result, fmt.Errorf("候補検証の対象は通常ファイルである必要があります")
	}
	touched = true // WriteFile may fail after changing the file.
	if err = store.WriteFile(path, after, info.Mode().Perm()); err != nil {
		return result, err
	}
	diff, err := git(ctx, in.Worktree, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD", "--", in.Relative)
	if err != nil {
		return result, err
	}
	if err = writeSharedArtifact(in.Config, artifact+".diff", []byte(diff), originalPath); err != nil {
		return result, err
	}
	if err = writeSharedArtifact(in.Config, in.Artifact+".diff", []byte(diff), originalPath); err != nil {
		return result, err
	}
	result.Checks = append(result.Checks, model.Check{Name: "変更案の適用", Status: "passed", Detail: "完全一致した箇所だけを変更し、文字コード・改行を保持しました"})
	if diff == "" {
		result.Checks = append(result.Checks, model.Check{Name: "変更差分", Status: "failed", Detail: "Gitで確認できる変更差分がありません"})
		return result, nil
	}
	legacy, err := in.Catalog.CheckLegacy(ctx, in.Config, path)
	result.Checks = append(result.Checks, legacy)
	if err != nil {
		return result, err
	}
	scope, err := scopeCheck(ctx, in.Worktree, in.Relative)
	result.Checks = append(result.Checks, scope)
	if err != nil {
		return result, err
	}
	if checksPass(result.Checks) {
		result.Checks = append(result.Checks, runChecks(ctx, in.Config, in.Source)...)
		finalScope, err := scopeCheck(ctx, in.Worktree, in.Relative)
		if err != nil {
			return result, err
		}
		if finalScope.Status != "passed" {
			finalScope.Name = "検証後の編集範囲"
			result.Checks = append(result.Checks, finalScope)
		}
		currentPath, pathErr := catalog.PathWithin(in.Source, in.File)
		var actual []byte
		var actualInfo os.FileInfo
		if pathErr == nil {
			actualInfo, pathErr = os.Lstat(currentPath)
			if pathErr == nil && actualInfo.Mode().IsRegular() {
				actual, pathErr = os.ReadFile(currentPath)
			}
		}
		if pathErr != nil || actualInfo == nil || !actualInfo.Mode().IsRegular() || actualInfo.Mode().Perm() != info.Mode().Perm() || digest(actual) != result.CandidateHash {
			result.Checks = append(result.Checks, model.Check{Name: "検証後の内容", Status: "failed", Detail: "検証コマンドが対象ファイルの内容・種類・権限を変更しました"})
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	result.Passed = checksPass(result.Checks)
	return result, nil
}

func candidateBaseUnchanged(ctx context.Context, in candidateValidationInput, path string) error {
	head, err := git(ctx, in.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if trim(head) != in.Head {
		return fmt.Errorf("候補検証の元コミットが変更されています。未知の変更は巻き戻しません")
	}
	status, err := git(ctx, in.Worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	dirty, err := hasSourceChanges(status)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("候補検証の開始前に作業コピーが変更されています。未知の変更は巻き戻しません")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if digest(current) != in.InputHash {
		return fmt.Errorf("候補検証の元ファイルが変更されています。対象を再確認してください")
	}
	return nil
}

func candidatePathInside(root, path string) bool {
	root, path = canonicalAlias(root), canonicalAlias(path)
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func candidateDiagnostics(checks []model.Check, filename string) []model.CandidateDiagnostic {
	result := []model.CandidateDiagnostic{}
	for _, check := range checks {
		if check.Status != "failed" {
			continue
		}
		key := map[string]string{"編集元のハッシュ": "base_hash", "変更案の適用": "apply_edits", "ファイルサイズ": "file_size", "変更差分": "candidate_diff", "旧シンボル残存": "legacy_symbols", "編集範囲": "edit_scope", "検証後の編集範囲": "edit_scope", "検証後の内容": "candidate_content", "候補検証": "validation"}[check.Name]
		if key == "" {
			key = "check_command"
		}
		lines := strings.Split(check.Detail, "\n")
		for i, line := range lines {
			if len(result) >= 20 {
				return result
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			diagnostic := model.CandidateDiagnostic{Check: key, Message: candidateDiagnosticText(line, 200)}
			if key == "legacy_symbols" && strings.HasPrefix(line, filename+":") {
				lineNumber, excerpt, found := strings.Cut(strings.TrimPrefix(line, filename+":"), ":")
				if number, err := strconv.Atoi(lineNumber); found && err == nil && number > 0 {
					diagnostic.LineBasis, diagnostic.Line = "candidate", number
					diagnostic.Excerpt = candidateDiagnosticText(excerpt, 200)
					diagnostic.Message = "旧シンボルの正規表現に一致する記述が残っています"
				}
			} else if key == "legacy_symbols" && i == 0 && len(lines) > 1 {
				continue // The parsed matches carry this shared explanation.
			}
			result = append(result, diagnostic)
		}
	}
	return result
}

func candidateDiagnosticText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text + "…"
}
