// Package catalog snapshots Markdown rules and discovers their scoped targets.
package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
)

var defaultExclusions = []string{
	".git/**", "**/.git/**", "node_modules/**", "**/node_modules/**",
	".onebyone/**", "**/.onebyone/**", "build/**", "**/build/**",
	"dist/**", "**/dist/**", "bin/**", "**/bin/**", "obj/**", "**/obj/**",
	".venv/**", "**/.venv/**", "AGENTS.md", "CLAUDE.md", "GEMINI.md", "SKILL.md",
	".codex/**", "**/.codex/**", ".agents/**", "**/.agents/**", ".claude/**", "**/.claude/**",
	".ssh/**", "**/.ssh/**", ".aws/**", "**/.aws/**", ".azure/**", "**/.azure/**",
	".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", ".npmrc", ".pypirc", ".netrc",
}

type Catalog struct {
	Rules           []model.Rule
	Hash            string
	SystemPrompt    string
	rg              string
	ruleBodies      map[string]string
	rulesRoot       string
	excludedRuleIDs []string
	excludedRules   map[string]bool
}

// Load freezes definitions for the entire run; later edits cannot alter an active review.
func Load(ctx context.Context, cfg model.Config) (_ *Catalog, err error) {
	defer func() {
		var diagnostic *DiagnosticError
		if err != nil && !errors.As(err, &diagnostic) {
			err = &DiagnosticError{Err: err}
		}
	}()
	rg, err := findRG(cfg.RGPath)
	if err != nil {
		return nil, err
	}
	rulesPath, err := checkedAbsolute(cfg.RulesPath)
	if err != nil {
		return nil, fmt.Errorf("rules.json: %w", err)
	}
	pkg, err := rulepack.Snapshot(rulesPath)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(pkg.Rules))
	for _, entry := range pkg.Rules {
		ids = append(ids, entry.ID)
	}
	excluded := model.NormalizeExcludedRuleIDs(cfg.ExcludedRuleIDs, ids)
	c := &Catalog{rg: rg, rulesRoot: rulesPath, ruleBodies: map[string]string{}, excludedRuleIDs: excluded, excludedRules: map[string]bool{}}
	for _, id := range excluded {
		c.excludedRules[id] = true
	}
	h := sha256.New()
	for _, entry := range pkg.Rules {
		d, err := ruleformat.Decode([]byte(entry.Markdown))
		if err != nil {
			return nil, ruleError(entry.ID, err)
		}
		rule := ruleformat.ToRule(entry.ID, d)
		if rule.PathPattern != "" {
			if err := c.validatePath(ctx, filepath.Dir(rulesPath), rule.PathPattern); err != nil {
				return nil, ruleError(entry.ID, err)
			}
		}
		if rule.ContentPattern != "" {
			if err := c.validate(ctx, []string{rule.ContentPattern}); err != nil {
				return nil, ruleError(entry.ID, err)
			}
		}
		c.Rules = append(c.Rules, rule)
		c.ruleBodies[entry.ID] = ruleformat.Markdown(d)
		fmt.Fprintf(h, "%d:%s%d:%s:selected=%t;", len(entry.ID), entry.ID, len(entry.Markdown), entry.Markdown, !c.excludedRules[entry.ID])
	}
	if len(c.Rules) == 0 {
		return nil, errors.New("ルールを1件以上追加してください")
	}
	c.Hash = hex.EncodeToString(h.Sum(nil))
	c.buildPrompt()
	return c, nil
}

func (c *Catalog) buildPrompt() {
	var common, index []string
	for _, rule := range c.Rules {
		if rule.Always {
			common = append(common, "### "+rule.ID+" | "+rule.Title+"\n"+c.ruleBodies[rule.ID])
		} else {
			index = append(index, fmt.Sprintf("- %s | %s | %s", rule.ID, rule.Title, rule.Summary))
		}
	}
	c.SystemPrompt = "## 対象ファイルに適用する共通ルール\n" + strings.Join(common, "\n\n---\n\n") +
		"\n\n## 対象ファイルの個別ルール（read_rule / read_rules で本文を取得）\n" + strings.Join(index, "\n") +
		"\n\n対象ルールは修正前のパスと本文の条件から確定しています。この一覧の全ルールを確認し、変更が必要か、既に満たすか、保留かを判断してください。対象外のルールは適用できません。条件が修正で消えてもレビュー対象は変わりません。\n"
}

// SelectedRuleCount excludes disabled definitions while Rules always retains
// the complete catalog for editing and selection UI.
func (c *Catalog) SelectedRuleCount() int { return len(c.Rules) - len(c.excludedRuleIDs) }

// ForRules confines tools, planning and independent review to the same frozen set.
func (c *Catalog) ForRules(ids []string) (*Catalog, error) {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	result := &Catalog{rg: c.rg, Hash: c.Hash, rulesRoot: c.rulesRoot, ruleBodies: map[string]string{}}
	for _, rule := range c.Rules {
		if !wanted[rule.ID] || c.excludedRules[rule.ID] {
			continue
		}
		result.Rules = append(result.Rules, rule)
		result.ruleBodies[rule.ID] = c.ruleBodies[rule.ID]
		delete(wanted, rule.ID)
	}
	if len(wanted) > 0 || len(result.Rules) == 0 {
		return nil, errors.New("対象ファイルのルールが見つかりません。対象を再抽出してください")
	}
	result.buildPrompt()
	return result, nil
}

func (c *Catalog) ReadRule(id string) (string, error) {
	body, ok := c.ruleBodies[id]
	if !ok {
		return "", fmt.Errorf("対象外または存在しないルールID: %q", id)
	}
	return body, nil
}

func (c *Catalog) validatePath(ctx context.Context, dir, pattern string) error {
	if strings.ContainsAny(pattern, "\\\r\n\x00") || strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "!") || strings.Contains(pattern, ":") {
		return errors.New("path_patternは / 区切りの相対globで指定してください（例: src/**/*.tsx）")
	}
	for _, part := range strings.Split(pattern, "/") {
		if part == ".." {
			return errors.New("path_patternに親フォルダは指定できません")
		}
	}
	_, err := c.run(ctx, dir, nil, "--files", "--hidden", "--no-ignore", "--no-config", "--glob", pattern, "--", ".")
	if err != nil {
		return fmt.Errorf("path_patternが不正です: %w", err)
	}
	return nil
}

func (c *Catalog) Scan(ctx context.Context, cfg model.Config) ([]model.Task, int, int, error) {
	ids := make([]string, 0, len(c.Rules))
	for _, rule := range c.Rules {
		ids = append(ids, rule.ID)
	}
	if strings.Join(model.NormalizeExcludedRuleIDs(cfg.ExcludedRuleIDs, ids), "\x00") != strings.Join(c.excludedRuleIDs, "\x00") {
		return nil, 0, 0, errors.New("適用ルールの選択が変更されています。ルールを読み直して対象抽出してください")
	}
	root, err := checkedAbsolute(cfg.Root)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("source root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, 0, 0, errors.New("source root must be a directory")
	}
	args := []string{"--files", "--null", "--hidden", "--no-ignore", "--no-config"}
	for _, glob := range defaultExclusions {
		args = append(args, "--iglob", "!"+glob)
	}
	for _, path := range []string{c.rulesRoot, cfg.QueuePath} {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, 0, 0, err
		}
		rel, err := filepath.Rel(root, absolute)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			glob := escapeGlob(filepath.ToSlash(rel))
			args = append(args, "--glob", "!"+glob, "--glob", "!"+glob+"/**")
		}
	}
	out, err := c.run(ctx, root, nil, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	allFiles := nulPaths(out)
	sort.Strings(allFiles)
	scanned := len(allFiles)
	rulesByFile := map[string][]string{}
	pathCache := map[string]map[string]bool{}
	for i := range c.Rules {
		rule := &c.Rules[i]
		rule.CandidateCount = 0
		if c.excludedRules[rule.ID] {
			continue
		}
		candidates := allFiles
		if rule.PathPattern != "" {
			matches, ok := pathCache[rule.PathPattern]
			if !ok {
				output, err := c.run(ctx, root, nil, "--files", "--null", "--hidden", "--no-ignore", "--no-config", "--glob", rule.PathPattern)
				if err != nil {
					return nil, scanned, 0, ruleError(rule.ID, err)
				}
				matches = map[string]bool{}
				for _, file := range nulPaths(output) {
					matches[file] = true
				}
				pathCache[rule.PathPattern] = matches
			}
			candidates = nil
			for _, file := range allFiles {
				if matches[file] {
					candidates = append(candidates, file)
				}
			}
		}
		if rule.ContentPattern != "" {
			matches, err := c.matchFiles(ctx, root, candidates, []string{rule.ContentPattern})
			if err != nil {
				return nil, scanned, 0, ruleError(rule.ID, err)
			}
			filtered := make([]string, 0, len(matches))
			for _, file := range candidates {
				if matches[file] {
					filtered = append(filtered, file)
				}
			}
			candidates = filtered
		}
		rule.CandidateCount = len(candidates)
		for _, file := range candidates {
			rulesByFile[file] = append(rulesByFile[file], rule.ID)
		}
	}
	files := make([]string, 0, len(rulesByFile))
	for _, file := range allFiles {
		if len(rulesByFile[file]) > 0 {
			files = append(files, file)
		}
	}
	tasks := make([]model.Task, 0, len(files))
	maxBytes := cfg.EffectiveMaxFileBytes()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, scanned, scanned - len(files), err
		}
		path, err := PathWithin(root, file)
		if err != nil {
			return nil, scanned, scanned - len(files), err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, scanned, scanned - len(files), fmt.Errorf("read source %s: %w", file, err)
		}
		stat, err := f.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			f.Close()
			return nil, scanned, scanned - len(files), fmt.Errorf("source is not a readable regular file: %s", file)
		}
		// Hash without keeping oversized inputs in memory; preserve their presence
		// in the ledger so an unsupported source cannot disappear silently.
		h := sha256.New()
		data, readErr := io.ReadAll(io.LimitReader(io.TeeReader(f, h), int64(maxBytes)+1))
		if readErr == nil && len(data) > maxBytes {
			_, readErr = io.Copy(h, f)
		}
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return nil, scanned, scanned - len(files), fmt.Errorf("read source %s: %w", file, errors.Join(readErr, closeErr))
		}
		task := model.Task{File: file, Rules: rulesByFile[file], Status: "pending", InputHash: hex.EncodeToString(h.Sum(nil)), UpdatedAt: now, RulesApplied: []string{}, History: []model.Attempt{}}
		switch {
		case len(data) > maxBytes:
			task.Status, task.Note = "needs_human", fmt.Sprintf("ファイルサイズが上限 %d bytes を超えています。", maxBytes)
		case bytes.IndexByte(data, 0) >= 0:
			task.Status, task.Note = "needs_human", "NULを含むバイナリまたは未対応の文字コードです。UTF-8ソースを使用してください。"
		case !utf8.Valid(data):
			task.Status, task.Note = "needs_human", "UTF-8以外の文字コードは自動編集できません。"
		}
		tasks = append(tasks, task)
	}

	return tasks, scanned, scanned - len(files), nil
}

func escapeGlob(path string) string {
	r := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]", "{", "\\{", "}", "\\}")
	return r.Replace(path)
}
