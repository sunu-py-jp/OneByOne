package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
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
	Plan      model.RepairPlan
	// Journal records CandidateHash after private artifacts are durable, before
	// the caller can use this result for review or serialized adoption.
	Journal func(model.CandidateValidation) error
}

// validateCandidate only reads the immutable input commit and writes private
// artifacts. It never writes or rolls back a worktree, so other files can be
// reviewed or committed while this candidate is checked. Worktree state and
// edit scope are checked later, under the runner's serialized adoption lock.
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
	defer func() {
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
	if err = candidateSnapshotMatches(ctx, in); err != nil {
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
	result.EditRanges, err = verifiedEditLineRanges(original.text, afterText, request.Edits)
	if err != nil {
		result.Checks = append(result.Checks, model.Check{Name: "修正箇所の対応付け", Status: "failed", Detail: err.Error()})
		return result, nil
	}
	if err = candidateHeldRangesUnchanged(in.Plan, result.EditRanges); err != nil {
		result.Checks = append(result.Checks, model.Check{Name: "保留箇所の保護", Status: "failed", Detail: err.Error()})
		return result, nil
	}
	result.AttributionVersion = model.LineAttributionVersion
	result.Checks = append(result.Checks, model.Check{Name: "修正箇所の対応付け", Status: "passed", Detail: "項目ごとのコード断片を照合し、実際の変更行だけを対応付けました"})
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
	diff, err := candidateSnapshotDiff(ctx, filepath.Dir(artifact), in.Relative, in.Before, after)
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
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	result.Passed = checksPass(result.Checks)
	return result, nil
}

// Attribution ranges describe actual changed lines, not the surrounding edit
// context. An intact quotation alone is insufficient if a candidate moved it.
func candidateHeldRangesUnchanged(plan model.RepairPlan, ranges []model.EditLineRange) error {
	for _, item := range plan.Items {
		if item.Status != "blocked" {
			continue
		}
		for _, location := range item.SourceLocations {
			for _, span := range ranges {
				if span.BeforeStart > 0 && span.BeforeStart <= location.EndLine && span.BeforeEnd >= location.StartLine {
					return fmt.Errorf("要確認の項目 %s の原文 %d〜%d 行は変更・移動できません。保留箇所を残した候補を作成してください", item.ID, location.StartLine, location.EndLine)
				}
			}
		}
	}
	return nil
}

func candidateSnapshotMatches(ctx context.Context, in candidateValidationInput) error {
	if !isImmutableCommitID(in.Head) {
		return fmt.Errorf("候補検証の元コミットIDが不正です")
	}
	// Read the pinned commit, never HEAD/the index/the live target. Concurrent
	// adoption of another file is allowed to move HEAD while an LLM works.
	original, err := git(ctx, in.Worktree, "show", "--no-ext-diff", "--no-textconv", in.Head+":"+in.Relative)
	if err != nil {
		return err
	}
	if digest([]byte(original)) != in.InputHash {
		return fmt.Errorf("候補検証の元ファイルが開始時のコミットと一致しません。対象を再確認してください")
	}
	return nil
}

func candidatePathInside(root, path string) bool {
	root, path = canonicalAlias(root), canonicalAlias(path)
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func candidateDiagnostics(checks []model.Check, _ string) []model.CandidateDiagnostic {
	result := []model.CandidateDiagnostic{}
	for _, check := range checks {
		if check.Status != "failed" {
			continue
		}
		key := map[string]string{"編集元のハッシュ": "base_hash", "変更案の適用": "apply_edits", "ファイルサイズ": "file_size", "変更差分": "candidate_diff", "編集範囲": "edit_scope", "検証後の編集範囲": "edit_scope", "検証後の内容": "candidate_content", "候補検証": "validation"}[check.Name]
		if key == "" {
			key = "validation"
		}
		lines := strings.Split(check.Detail, "\n")
		for _, line := range lines {
			if len(result) >= 20 {
				return result
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			diagnostic := model.CandidateDiagnostic{Check: key, Message: candidateDiagnosticText(line, 200)}
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
