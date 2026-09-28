package engine

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"onebyone/internal/model"
)

var publicationReportPathPattern = regexp.MustCompile(`^OneByOne/[0-9]{14}_results\.md$`)

type publicationTreeEntry struct {
	mode, kind, hash, name string
}

// Build immutable Git objects directly. Neither the user's checkout/index nor
// the internal worktree receives a temporary report or a staging operation.
func createPublicationReport(ctx context.Context, repo, source, message string, files []model.ResultPublicationFile, timestamp time.Time) (tree, reportPath, blob string, err error) {
	root, err := readPublicationTree(ctx, repo, source+"^{tree}")
	if err != nil {
		return "", "", "", err
	}
	entries, err := publicationReportDirectory(ctx, repo, root)
	if err != nil {
		return "", "", "", err
	}
	occupied := map[string]bool{}
	for _, entry := range entries {
		occupied[strings.ToLower(entry.name)] = true
	}
	// Retain the requested timestamp-only filename without overwriting an
	// existing report when another result already used the same second.
	for {
		if err := ctx.Err(); err != nil {
			return "", "", "", err
		}
		name := timestamp.Format("20060102150405") + "_results.md"
		if !occupied[strings.ToLower(name)] {
			reportPath = "OneByOne/" + name
			break
		}
		timestamp = timestamp.Add(time.Second)
	}
	blob, err = publicationGitInput(ctx, repo, publicationReportContent(message, files), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", "", "", err
	}
	blob = trim(blob)
	tree, err = publicationTreeWithReport(ctx, repo, source, reportPath, blob)
	return tree, reportPath, blob, err
}

func readPublicationTree(ctx context.Context, repo, tree string) ([]publicationTreeEntry, error) {
	data, err := git(ctx, repo, "ls-tree", "-z", tree)
	if err != nil {
		return nil, err
	}
	entries := []publicationTreeEntry{}
	for _, record := range zeroLines(data) {
		metadata, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || name == "" || strings.Contains(name, "/") || !isImmutableCommitID(fields[2]) {
			return nil, fmt.Errorf("コミット対象のGit treeを読み込めません")
		}
		entries = append(entries, publicationTreeEntry{fields[0], fields[1], fields[2], name})
	}
	return entries, nil
}

func publicationReportDirectory(ctx context.Context, repo string, root []publicationTreeEntry) ([]publicationTreeEntry, error) {
	directory := ""
	for _, entry := range root {
		if !strings.EqualFold(entry.name, "OneByOne") {
			continue
		}
		if entry.name != "OneByOne" || entry.kind != "tree" || entry.mode != "040000" {
			return nil, fmt.Errorf("結果ファイルを保存できません: リポジトリ直下の %s が OneByOne フォルダと競合しています", entry.name)
		}
		directory = entry.hash
	}
	if directory != "" {
		return readPublicationTree(ctx, repo, directory)
	}
	return []publicationTreeEntry{}, nil
}

func writePublicationTree(ctx context.Context, repo string, entries []publicationTreeEntry) (string, error) {
	var input strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&input, "%s %s %s\t%s\x00", entry.mode, entry.kind, entry.hash, entry.name)
	}
	tree, err := publicationGitInput(ctx, repo, input.String(), "mktree", "-z")
	return trim(tree), err
}

// This also reconstructs the expected tree during crash recovery: exactly one
// new regular report file is permitted in addition to the adopted source tree.
func publicationTreeWithReport(ctx context.Context, repo, source, reportPath, blob string) (string, error) {
	if !publicationReportPathPattern.MatchString(reportPath) || !isImmutableCommitID(blob) {
		return "", fmt.Errorf("結果ファイルの記録が不正です")
	}
	kind, err := git(ctx, repo, "cat-file", "-t", blob)
	if err != nil || trim(kind) != "blob" {
		return "", fmt.Errorf("結果ファイルのGitオブジェクトを確認できません")
	}
	root, err := readPublicationTree(ctx, repo, source+"^{tree}")
	if err != nil {
		return "", err
	}
	entries, err := publicationReportDirectory(ctx, repo, root)
	if err != nil {
		return "", err
	}
	name := path.Base(reportPath)
	for _, entry := range entries {
		if strings.EqualFold(entry.name, name) {
			return "", fmt.Errorf("結果ファイルが既に存在します: %s", reportPath)
		}
	}
	entries = append(entries, publicationTreeEntry{"100644", "blob", blob, name})
	subtree, err := writePublicationTree(ctx, repo, entries)
	if err != nil {
		return "", err
	}
	replaced := false
	for i := range root {
		if root[i].name == "OneByOne" {
			root[i].hash = subtree
			replaced = true
		}
	}
	if !replaced {
		root = append(root, publicationTreeEntry{"040000", "tree", subtree, "OneByOne"})
	}
	return writePublicationTree(ctx, repo, root)
}

// Generated file links are repo-relative in the UI/commit body. In the report,
// adjust their standalone lines and summary-table cells for OneByOne/.
// Preserve the user's other Markdown, including fenced examples and whitespace.
func publicationReportContent(message string, files []model.ResultPublicationFile) string {
	links := map[string]string{}
	for _, file := range files {
		linkPath := file.LinkPath
		if linkPath == "" {
			continue
		}
		for _, table := range []bool{false, true} {
			links[publicationFileLink(linkPath, linkPath, table)] = publicationFileLink(linkPath, "../"+linkPath, table)
		}
	}
	lines := strings.SplitAfter(message, "\n")
	fence := byte(0)
	fenceLength := 0
	for i, line := range lines {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		trimmed := strings.TrimLeft(content, " ")
		if len(content)-len(trimmed) <= 3 && len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			length := 0
			for length < len(trimmed) && trimmed[length] == trimmed[0] {
				length++
			}
			if length >= 3 {
				if fence == 0 {
					fence, fenceLength = trimmed[0], length
				} else if fence == trimmed[0] && length >= fenceLength && strings.TrimSpace(trimmed[length:]) == "" {
					fence, fenceLength = 0, 0
				}
				continue
			}
		}
		if replacement, ok := links[content]; ok && fence == 0 {
			lines[i] = replacement + line[len(content):]
		} else if fence == 0 && strings.HasPrefix(content, "| ") {
			if cell, rest, found := strings.Cut(content[2:], " | "); found {
				if replacement, ok := links[cell]; ok {
					lines[i] = "| " + replacement + " | " + rest + line[len(content):]
				}
			}
		}
	}
	return strings.Join(lines, "")
}
