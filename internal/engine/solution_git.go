package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"onebyone/internal/catalog"
	"onebyone/internal/store"
)

// prepareSolutionInputs pins the solution to raw committed bytes. Git's normal
// text/eol conversion is allowed during checkout, but custom filters and
// working-tree encodings cannot change the content that is built and reviewed.
func prepareSolutionInputs(ctx context.Context, source, worktree, head, sourceRelative string, files []string) (map[string][]byte, map[string]string, error) {
	original, texts := map[string][]byte{}, map[string]string{}
	type inputFile struct {
		path string
		mode os.FileMode
		data []byte
	}
	writes := []inputFile{}
	for _, file := range files {
		if forbiddenContext(file) {
			return nil, nil, fmt.Errorf("設定・認証情報はソリューションの編集対象にできません: %s", file)
		}
		path, err := catalog.PathWithin(source, file)
		if err != nil {
			return nil, nil, err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("ソリューション入力が通常ファイルではありません: %s", file)
		}
		rel := filepath.ToSlash(filepath.Join(sourceRelative, file))
		if err := requireSolutionPlainAttributes(ctx, worktree, rel); err != nil {
			return nil, nil, err
		}
		blob, err := readSnapshotFile(ctx, worktree, head, rel)
		if err != nil {
			return nil, nil, err
		}
		decoded, err := decodeSource(blob)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", file, err)
		}
		checkout, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		checkoutText, err := decodeSource(checkout)
		if err != nil || checkoutText.text != decoded.text {
			return nil, nil, fmt.Errorf("Gitチェックアウトが入力内容を変更しました。filter / working-tree-encoding を使用しない作業コピーを指定してください: %s", file)
		}
		original[file], texts[file] = blob, decoded.text
		writes = append(writes, inputFile{path: path, mode: info.Mode().Perm(), data: blob})
	}
	// Validate every file before normalizing the disposable scratch copy.
	for _, file := range writes {
		if err := store.WriteFile(file.path, file.data, file.mode); err != nil {
			return nil, nil, err
		}
	}
	return original, texts, nil
}

// stageSolutionCandidate writes blobs without clean/EOL filters, retaining the
// original tracked mode. It returns a tree whose blobs are exactly the bytes
// that the engine just built and reviewed.
func stageSolutionCandidate(ctx context.Context, scratch, head, relative string, files []string, expected map[string][]byte) (string, error) {
	if !isImmutableCommitID(head) {
		return "", fmt.Errorf("開始コミットが不正です")
	}
	type candidateFile struct {
		path, mode string
		data       []byte
	}
	inputs := []candidateFile{}
	seen := map[string]bool{}
	for _, file := range files {
		if seen[file] {
			return "", fmt.Errorf("ステージ対象が重複しています: %s", file)
		}
		seen[file] = true
		data, ok := expected[file]
		if !ok {
			return "", fmt.Errorf("検証済みのソリューション入力にないファイルです: %s", file)
		}
		if _, err := catalog.PathWithin(filepath.Join(scratch, relative), file); err != nil {
			return "", err
		}
		rel := filepath.ToSlash(filepath.Join(relative, file))
		if err := requireSolutionPlainAttributes(ctx, scratch, rel); err != nil {
			return "", err
		}
		mode, err := solutionBlobMode(ctx, scratch, head, rel)
		if err != nil {
			return "", err
		}
		inputs = append(inputs, candidateFile{path: rel, mode: mode, data: data})
	}
	if _, err := git(ctx, scratch, "read-tree", head); err != nil {
		return "", err
	}
	for _, file := range inputs {
		hash, err := publicationGitInput(ctx, scratch, string(file.data), "hash-object", "-w", "--stdin", "--no-filters")
		if err != nil {
			return "", err
		}
		if !isImmutableCommitID(trim(hash)) {
			return "", fmt.Errorf("候補blobのIDが不正です")
		}
		if _, err := git(ctx, scratch, "update-index", "--cacheinfo", file.mode, trim(hash), file.path); err != nil {
			return "", err
		}
	}
	tree, err := git(ctx, scratch, "write-tree")
	if err != nil {
		return "", err
	}
	return trim(tree), nil
}

func solutionBlobMode(ctx context.Context, repo, head, path string) (string, error) {
	listing, err := git(ctx, repo, "ls-tree", "-z", head, "--", path)
	if err != nil {
		return "", err
	}
	entries := zeroLines(listing)
	if len(entries) != 1 {
		return "", fmt.Errorf("Gitに登録された通常ファイルではありません: %s", path)
	}
	parts := strings.SplitN(entries[0], "\t", 2)
	fields := strings.Fields(parts[0])
	if len(parts) != 2 || parts[1] != path || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return "", fmt.Errorf("Gitに登録された通常ファイルではありません: %s", path)
	}
	return fields[0], nil
}

func requireSolutionPlainAttributes(ctx context.Context, repo, path string) error {
	if err := validatePreviewFilePath(path); err != nil {
		return err
	}
	value, err := git(ctx, repo, "check-attr", "-z", "filter", "working-tree-encoding", "--", path)
	if err != nil {
		return err
	}
	fields := zeroLines(value)
	if len(fields) != 6 {
		return fmt.Errorf("Gitのファイル属性を確認できません: %s", path)
	}
	seen := map[string]bool{}
	for i := 0; i < len(fields); i += 3 {
		if fields[i] != path || (fields[i+1] != "filter" && fields[i+1] != "working-tree-encoding") || seen[fields[i+1]] {
			return fmt.Errorf("Gitのファイル属性が不正です: %s", path)
		}
		seen[fields[i+1]] = true
		if fields[i+2] != "unspecified" && fields[i+2] != "unset" {
			return fmt.Errorf("ソリューション入力にはGitの%s属性を使用できません。内容を変換しない作業コピーを指定してください: %s", fields[i+1], path)
		}
	}
	return nil
}
