// Package catalog loads migration rules and uses ripgrep for both discovery and
// verification. The expressions used at the entrance and exit are identical.
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
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
)

var defaultExclusions = []string{
	".git/**", "**/.git/**", "node_modules/**", "**/node_modules/**",
	"*.oborules", "**/*.oborules",
	".onebyone/**", "**/.onebyone/**", "build/**", "**/build/**",
	"dist/**", "**/dist/**", "bin/**", "**/bin/**", "obj/**", "**/obj/**",
	".venv/**", "**/.venv/**", "AGENTS.md", "CLAUDE.md", "GEMINI.md", "SKILL.md",
	".codex/**", "**/.codex/**", ".agents/**", "**/.agents/**", ".claude/**", "**/.claude/**",
	".ssh/**", "**/.ssh/**", ".aws/**", "**/.aws/**", ".azure/**", "**/.azure/**",
	".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", ".npmrc", ".pypirc", ".netrc",
}

type Catalog struct {
	Rules        []model.Rule
	Hash         string
	SystemPrompt string
	rg           string
	legacy       []string
	ruleBodies   map[string]string
	rulesRoot    string
}

// Load snapshots rule definitions once. ReadRule serves that snapshot, ensuring
// an on-disk edit during a run cannot silently change the active instructions.
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
	rulesRoot, err := checkedAbsolute(cfg.RulesPath)
	if err != nil {
		return nil, fmt.Errorf("rules directory: %w", err)
	}
	entries, err := os.ReadDir(rulesRoot)
	if err != nil {
		return nil, fmt.Errorf("read rules directory: %w", err)
	}
	c := &Catalog{rg: rg, rulesRoot: rulesRoot, ruleBodies: map[string]string{}}
	h := sha256.New()
	var always, indexes []string
	idRE := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink in rules directory: %s", entry.Name())
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		id := entry.Name()
		if !idRE.MatchString(id) {
			return nil, &DiagnosticError{RuleID: id, Err: fmt.Errorf("invalid rule ID %q: use letters, digits, hyphens or underscores", id)}
		}
		ruleDir := filepath.Join(rulesRoot, id)
		assets, err := os.ReadDir(ruleDir)
		if err != nil {
			return nil, ruleError(id, err)
		}
		hasDefinition := false
		for _, asset := range assets {
			switch strings.ToLower(asset.Name()) {
			case "rule.md", "pattern.txt", "name.txt":
				return nil, ruleError(id, fmt.Errorf("旧形式の%sは使用できません。rule.jsonに項目を保存してください", asset.Name()))
			case "rule.json":
				if asset.Name() != "rule.json" {
					return nil, ruleError(id, errors.New("定義ファイル名はrule.jsonにしてください"))
				}
				hasDefinition = true
			}
		}
		if !hasDefinition {
			return nil, ruleError(id, errors.New("rule.jsonが必要です"))
		}
		text, err := readDefinition(filepath.Join(ruleDir, "rule.json"))
		if err != nil {
			return nil, ruleError(id, err)
		}
		definition, err := ruleformat.Decode([]byte(text))
		if err != nil {
			return nil, ruleError(id, err)
		}
		rule := ruleformat.ToRule(id, definition)
		if !rule.Always {
			if err := c.validate(ctx, []string{strings.TrimSpace(definition.Pattern)}); err != nil {
				return nil, ruleError(id, err)
			}
		}
		body := ruleformat.Markdown(definition)
		c.Rules = append(c.Rules, rule)
		c.ruleBodies[id] = body
		fmt.Fprintf(h, "%d:%s%d:%s", len(id), id, len(text), text)
		if rule.Always {
			always = append(always, "### "+id+" | "+rule.Title+"\n"+body)
		} else {
			indexes = append(indexes, fmt.Sprintf("- %s | %s | %s | rules/%s/rule.json", id, rule.Title, rule.Summary, id))
		}
	}
	if len(c.Rules) == 0 {
		return nil, errors.New("no rule directories found; expected rules/<ID>/rule.json")
	}
	if cfg.LegacyPath != "" {
		legacy, err := readDefinition(cfg.LegacyPath)
		if err != nil {
			return nil, filteringError(fmt.Errorf("legacy symbols: %w", err))
		}
		c.legacy = patternLines(legacy)
		if len(c.legacy) == 0 {
			return nil, filteringError(errors.New("legacy-symbols.txt must contain at least one nonblank expression when enabled"))
		}
		if err := c.validate(ctx, c.legacy); err != nil {
			return nil, filteringError(fmt.Errorf("legacy symbols: %w", err))
		}
		fmt.Fprintf(h, "legacy:%d:%s", len(legacy), legacy)
	}
	c.Hash = hex.EncodeToString(h.Sum(nil))
	c.SystemPrompt = "## 全ファイル共通で必ず適用するルール\n" + strings.Join(always, "\n\n---\n\n") +
		"\n\n## 個別ルール（必要なルール本文を read_rule で取得してから適用）\n" + strings.Join(indexes, "\n") +
		"\n\n候補ルールはファイル本文の正規表現一致による参考情報です。実際の適用はコードとルール本文で判断してください。候補外の個別ルールも必要なら取得できます。\n"
	return c, nil
}

func (c *Catalog) ReadRule(id string) (string, error) {
	body, ok := c.ruleBodies[id]
	if !ok {
		return "", fmt.Errorf("unknown rule ID: %q", id)
	}
	return body, nil
}

func (c *Catalog) Scan(ctx context.Context, cfg model.Config) ([]model.Task, int, int, error) {
	root, err := checkedAbsolute(cfg.Root)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("source root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, 0, 0, errors.New("source root must be a directory")
	}
	args := []string{"--files", "--null", "--hidden", "--no-ignore", "--no-config"}
	for _, glob := range cfg.IncludeGlobs {
		if strings.TrimSpace(glob) != "" {
			args = append(args, "--glob", glob)
		}
	}
	for _, glob := range defaultExclusions {
		// Protect configuration and credential paths regardless of filename case
		// on Windows and on case-insensitive macOS volumes.
		args = append(args, "--iglob", "!"+glob)
	}
	for _, glob := range cfg.ExcludeGlobs {
		if strings.TrimSpace(glob) != "" {
			args = append(args, "--glob", "!"+strings.TrimPrefix(glob, "!"))
		}
	}
	// Prevent the migration's own inputs and ledger from becoming source tasks.
	for _, path := range []string{c.rulesRoot, cfg.LegacyPath, cfg.QueuePath} {
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
	files := nulPaths(out)
	sort.Strings(files)
	scanned := len(files)
	// A common rule applies to every source within the configured file scope.
	// Legacy symbols remain a post-edit gate, but cannot hide common-rule work
	// simply because a file has no old API reference.
	hasCommonRule := false
	for _, rule := range c.Rules {
		if rule.Always {
			hasCommonRule = true
			break
		}
	}
	if !hasCommonRule && len(c.legacy) != 0 {
		matched, err := c.matchFiles(ctx, root, files, c.legacy)
		if err != nil {
			return nil, scanned, 0, fmt.Errorf("legacy discovery: %w", err)
		}
		var filtered []string
		for _, file := range files {
			if matched[file] {
				filtered = append(filtered, file)
			}
		}
		files = filtered
	}
	tasks := make([]model.Task, 0, len(files))
	byFile := make(map[string]int, len(files))
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
		task := model.Task{File: file, Rules: []string{}, Status: "pending", InputHash: hex.EncodeToString(h.Sum(nil)), UpdatedAt: now, RulesApplied: []string{}, History: []model.Attempt{}}
		switch {
		case len(data) > maxBytes:
			task.Status, task.Note = "needs_human", fmt.Sprintf("ファイルサイズが上限 %d bytes を超えています。", maxBytes)
		case bytes.IndexByte(data, 0) >= 0:
			task.Status, task.Note = "needs_human", "NULを含むバイナリまたは未対応の文字コードです。UTF-8ソースを使用してください。"
		case !utf8.Valid(data):
			task.Status, task.Note = "needs_human", "UTF-8以外の文字コードは自動編集できません。"
		}
		byFile[file] = len(tasks)
		tasks = append(tasks, task)
	}
	for i := range c.Rules {
		c.Rules[i].CandidateCount = 0
		if c.Rules[i].Always {
			c.Rules[i].CandidateCount = len(tasks)
			continue
		}
		matches, err := c.matchFiles(ctx, root, files, patternLines(c.Rules[i].Pattern))
		if err != nil {
			return nil, scanned, scanned - len(files), fmt.Errorf("match rule %s: %w", c.Rules[i].ID, err)
		}
		for file := range matches {
			index, ok := byFile[file]
			if !ok {
				return nil, scanned, scanned - len(files), fmt.Errorf("rg returned unexpected path %q", file)
			}
			tasks[index].Rules = append(tasks[index].Rules, c.Rules[i].ID)
			c.Rules[i].CandidateCount++
		}
	}
	return tasks, scanned, scanned - len(files), nil
}

func (c *Catalog) CheckLegacy(ctx context.Context, cfg model.Config, path string) (model.Check, error) {
	started := time.Now()
	check := model.Check{Name: "旧シンボル残存", Status: "skipped", Detail: "旧シンボル定義が指定されていません。"}
	if len(c.legacy) == 0 {
		return check, nil
	}
	// The runner supplies the isolated worktree path, which can differ from Root.
	absolute, err := checkedAbsolute(path)
	if err != nil {
		check.Status, check.Detail = "failed", err.Error()
		return check, err
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		err = fmt.Errorf("legacy check target is not a regular file: %s", path)
		check.Status, check.Detail = "failed", err.Error()
		return check, err
	}
	args := []string{"--no-config", "--text", "--encoding", "none", "--color", "never", "--line-number", "--with-filename", "--no-heading", "--max-count", "20", "--max-columns", "300", "--max-columns-preview"}
	for _, pattern := range c.legacy {
		args = append(args, "-e", pattern)
	}
	args = append(args, "--", filepath.Base(absolute))
	matches, err := c.run(ctx, filepath.Dir(absolute), nil, args...)
	check.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		check.Status, check.Detail = "failed", err.Error()
		return check, err
	}
	if len(matches) != 0 {
		check.Status, check.Detail = "failed", "旧シンボルの正規表現に一致する記述が残っています（コメントも検査対象、最大20行）。\n"+strings.TrimSpace(string(matches))
	} else {
		check.Status, check.Detail = "passed", "旧シンボルの一致はありません。"
	}
	return check, nil
}

func readDefinition(path string) (string, error) {
	abs, err := checkedAbsolute(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("definition must be a regular file: %s", path)
	}
	if info.Size() > 4*1024*1024 {
		return "", fmt.Errorf("definition exceeds 4 MiB: %s", path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("definition must be UTF-8 text: %s", path)
	}
	return strings.TrimPrefix(string(data), "\ufeff"), nil
}

func patternLines(text string) []string {
	var patterns []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			patterns = append(patterns, line)
		}
	}
	return patterns
}

func escapeGlob(path string) string {
	r := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]", "{", "\\{", "}", "\\}")
	return r.Replace(path)
}
