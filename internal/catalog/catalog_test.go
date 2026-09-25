package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
)

func fixture(t *testing.T) model.Config {
	t.Helper()
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep is required for catalog integration tests")
	}
	dir := t.TempDir()
	cfg := model.Config{Root: filepath.Join(dir, "source"), RulesPath: filepath.Join(dir, "rules"), RGPath: rg, MaxFileBytes: 1024}
	if err := os.MkdirAll(cfg.Root, 0755); err != nil {
		t.Fatal(err)
	}
	writeRule(t, cfg, "R001", model.RuleDefinition{Version: 1, Name: "共通のルール", Overview: "既存の動作を保持します。", Pattern: " "})
	writeRule(t, cfg, "R019", model.RuleDefinition{Version: 1, Name: "保存処理の変更", Overview: "OldClient.Save を NewClient.Persist に移行します。\n別の行にも具体的なキーワードがあります。", Before: "individual before example", After: "individual after example", Pattern: `\.Save\s*\(`})
	return cfg
}

func writeRule(t *testing.T, cfg model.Config, id string, definition model.RuleDefinition) {
	t.Helper()
	data, err := ruleformat.Encode(definition)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cfg.RulesPath, id, "rule.json"), string(data))
}
func updateRule(t *testing.T, cfg model.Config, id string, update func(*model.RuleDefinition)) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cfg.RulesPath, id, "rule.json"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := ruleformat.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	update(&d)
	writeRule(t, cfg, id, d)
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRuleCatalogAndBodyMatching(t *testing.T) {
	cfg := fixture(t)
	write(t, filepath.Join(cfg.Root, "a.ext"), "OldClient.Save (options);\r\n")
	write(t, filepath.Join(cfg.Root, "b.Save.ext"), "NewClient.Persist(options);\n")
	write(t, filepath.Join(cfg.Root, "with space.ext"), "\ufeffOldClient.Save(options);\r\n")
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rules) != 2 || !c.Rules[0].Always || c.Rules[1].Always {
		t.Fatalf("wrong classification: %+v", c.Rules)
	}
	if !strings.Contains(c.SystemPrompt, "既存の動作を保持") || !strings.Contains(c.SystemPrompt, "OldClient.Save") || !strings.Contains(c.SystemPrompt, "rules/R019/rule.json") {
		t.Fatalf("missing rule context: %s", c.SystemPrompt)
	}
	if strings.Contains(c.SystemPrompt, "individual before example") {
		t.Fatal("individual rule body should be loaded on demand")
	}
	tasks, scanned, excluded, err := c.Scan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 3 || excluded != 0 || len(tasks) != 3 {
		t.Fatalf("counts: %d, %d, %d", scanned, excluded, len(tasks))
	}
	if !reflect.DeepEqual(tasks[0].Rules, []string{"R019"}) || len(tasks[1].Rules) != 0 || tasks[1].Status != "pending" || tasks[2].Status != "pending" {
		t.Fatalf("must match body and allow empty candidate list / BOM: %+v", tasks)
	}
	hash := sha256.Sum256([]byte("OldClient.Save (options);\r\n"))
	if tasks[0].InputHash != hex.EncodeToString(hash[:]) {
		t.Fatal("source hash must preserve original bytes")
	}
	body, err := c.ReadRule("R019")
	if err != nil || !strings.Contains(body, "# 変更前") {
		t.Fatal("ReadRule should return complete body")
	}
	write(t, filepath.Join(cfg.RulesPath, "R019", "rule.json"), "changed during execution")
	unchanged, _ := c.ReadRule("R019")
	if unchanged != body {
		t.Fatal("active rule definitions must be immutable snapshots")
	}
	if _, err := c.ReadRule("../R019"); err == nil {
		t.Fatal("rule lookup must not accept arbitrary paths")
	}
}

func TestLegacyDiscoveryUsesSameCheck(t *testing.T) {
	cfg := fixture(t)
	// With only individual rules, legacy discovery still narrows the queue.
	if err := os.RemoveAll(filepath.Join(cfg.RulesPath, "R001")); err != nil {
		t.Fatal(err)
	}
	cfg.LegacyPath = filepath.Join(filepath.Dir(cfg.Root), "patterns", "legacy-symbols.txt")
	write(t, cfg.LegacyPath, "\ufeff\\bOldClient\\b\r\n\r\n\\bGoneType\\b\r\n")
	write(t, filepath.Join(cfg.Root, "a.ext"), "OldClient.Save(options);\n")
	write(t, filepath.Join(cfg.Root, "b.ext"), "NewClient.Persist(options);\n")
	write(t, filepath.Join(cfg.Root, "comment.ext"), "// GoneType is obsolete\n")
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, scanned, excluded, err := c.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 2 || scanned != 3 || excluded != 1 {
		t.Fatalf("legacy discovery: %+v %d %d %v", tasks, scanned, excluded, err)
	}
	for _, task := range tasks {
		check, err := c.CheckLegacy(context.Background(), cfg, filepath.Join(cfg.Root, task.File))
		if err != nil || check.Status != "failed" {
			t.Fatalf("discovery and gate disagree: %+v %v", check, err)
		}
		if !strings.Contains(check.Detail, task.File+":1:") {
			t.Fatalf("check must identify the matching source location: %+v", check)
		}
	}
	write(t, filepath.Join(cfg.Root, "a.ext"), "NewClient.Persist(options);\n")
	check, err := c.CheckLegacy(context.Background(), cfg, filepath.Join(cfg.Root, "a.ext"))
	if err != nil || check.Status != "passed" {
		t.Fatalf("migrated source must pass: %+v %v", check, err)
	}
	// Mid-run edits to definitions must not weaken the active gate.
	write(t, cfg.LegacyPath, "NeverMatches")
	check, err = c.CheckLegacy(context.Background(), cfg, filepath.Join(cfg.Root, "comment.ext"))
	if err != nil || check.Status != "failed" {
		t.Fatal("legacy patterns changed during the active run")
	}
}

func TestInvalidDefinitionsFailEarly(t *testing.T) {
	for _, which := range []string{"missing-definition", "invalid-rule-regex", "invalid-legacy-regex", "blank-legacy"} {
		t.Run(which, func(t *testing.T) {
			cfg := fixture(t)
			switch which {
			case "missing-definition":
				if err := os.Remove(filepath.Join(cfg.RulesPath, "R001", "rule.json")); err != nil {
					t.Fatal(err)
				}
			case "invalid-rule-regex":
				updateRule(t, cfg, "R019", func(d *model.RuleDefinition) { d.Pattern = "(" })
			case "invalid-legacy-regex", "blank-legacy":
				cfg.LegacyPath = filepath.Join(filepath.Dir(cfg.Root), "legacy.txt")
				pattern := "["
				if which == "blank-legacy" {
					pattern = "\n \n"
				}
				write(t, cfg.LegacyPath, pattern)
			}
			if _, err := Load(context.Background(), cfg); err == nil {
				t.Fatal("invalid definitions must fail before creating a queue")
			}
		})
	}
}

func TestScopeExclusionsAndIgnoreIndependence(t *testing.T) {
	cfg := fixture(t)
	cfg.IncludeGlobs = []string{"*.ext", "*.md", ".env*", "*.pem", "*.key"}
	cfg.ExcludeGlobs = []string{"skip/**"}
	for _, file := range []string{"src/a.ext", "src/ignored.ext", "node_modules/a.ext", "build/a.ext", "deep/obj/a.ext", "skip/a.ext", ".onebyone/results/a.ext", "AGENTS.md", "deep/CLAUDE.md", "deep/mixed/Agents.md", ".agents/policy.md", ".codex/config.ext", ".env", "deep/.env.local", "cert.pem", "key.key"} {
		write(t, filepath.Join(cfg.Root, file), "OldClient.Save(options);\n")
	}
	write(t, filepath.Join(cfg.Root, ".gitignore"), "src/ignored.ext\n")
	// A hostile global rg config must not change the configured discovery scope.
	rgConfig := filepath.Join(filepath.Dir(cfg.Root), "rg.config")
	write(t, rgConfig, "--glob=!src/**\n")
	t.Setenv("RIPGREP_CONFIG_PATH", rgConfig)
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, _, err := c.Scan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, task := range tasks {
		got = append(got, task.File)
	}
	if !reflect.DeepEqual(got, []string{"src/a.ext", "src/ignored.ext"}) {
		t.Fatalf("unexpected scope: %v", got)
	}
}

func TestUnsupportedSourcesRemainVisible(t *testing.T) {
	cfg := fixture(t)
	cfg.LegacyPath = filepath.Join(filepath.Dir(cfg.Root), "legacy.txt")
	write(t, cfg.LegacyPath, "OldClient")
	write(t, filepath.Join(cfg.Root, "binary.ext"), "OldClient\x00Save")
	write(t, filepath.Join(cfg.Root, "encoding.ext"), "OldClient\xff")
	write(t, filepath.Join(cfg.Root, "large.ext"), "OldClient"+strings.Repeat("a", 2048))
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, _, err := c.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 3 {
		t.Fatalf("unsupported files were silently dropped: %+v %v", tasks, err)
	}
	for _, task := range tasks {
		if task.Status != "needs_human" || task.Note == "" || len(task.InputHash) != 64 {
			t.Fatalf("missing review reason or byte hash: %+v", task)
		}
	}
	data, err := os.ReadFile(filepath.Join(cfg.Root, "large.ext"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if tasks[2].InputHash != hex.EncodeToString(hash[:]) {
		t.Fatal("oversized file must be completely hashed")
	}
}

func TestPathTraversalAndSymlinks(t *testing.T) {
	cfg := fixture(t)
	write(t, filepath.Join(cfg.Root, "safe", "file.ext"), "source")
	for _, path := range []string{"", "../outside", "safe/../../outside", `safe\..\..\outside`, "/absolute", `C:\file.ext`, `\\server\share\file.ext`, "safe/file.ext\x00", "safe/file.ext:stream", "safe/file.ext.", "safe/file.ext ", "safe/NUL.txt", "safe/COM1", "safe/LPT9.log"} {
		if _, err := PathWithin(cfg.Root, path); err == nil {
			t.Errorf("unsafe path accepted: %q", path)
		}
	}
	if _, err := PathWithin(cfg.Root, "safe/file.ext"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require developer mode on Windows")
	}
	outside := filepath.Join(filepath.Dir(cfg.Root), "outside")
	write(t, filepath.Join(outside, "secret.ext"), "OldClient.Save(options)")
	if err := os.Symlink(outside, filepath.Join(cfg.Root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := PathWithin(cfg.Root, "link/secret.ext"); err == nil {
		t.Fatal("symlink parent must be rejected")
	}
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, _, err := c.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 1 || tasks[0].File != "safe/file.ext" {
		t.Fatalf("scan must not follow symlinks: %+v %v", tasks, err)
	}
	if err := os.Symlink(filepath.Join(cfg.RulesPath, "R019"), filepath.Join(cfg.RulesPath, "R020")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), cfg); err == nil {
		t.Fatal("symlinked rule directories must be rejected")
	}
}

func TestMissingFileIsAnErrorNotANoMatch(t *testing.T) {
	cfg := fixture(t)
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.matchFiles(context.Background(), cfg.Root, []string{"missing.ext"}, []string{"OldClient"}); err == nil {
		t.Fatal("rg exit 2 must propagate as an error, not an empty match set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := c.Scan(ctx, cfg); err == nil {
		t.Fatal("cancelled scan must stop")
	}
}

func TestCatalogHashChangesWithRulesAndLegacy(t *testing.T) {
	cfg := fixture(t)
	a, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(context.Background(), cfg)
	if err != nil || a.Hash != b.Hash {
		t.Fatal("identical definitions should have deterministic hashes")
	}
	updateRule(t, cfg, "R019", func(d *model.RuleDefinition) { d.Pattern = `\.Different\(` })
	b, err = Load(context.Background(), cfg)
	if err != nil || a.Hash == b.Hash {
		t.Fatal("pattern changes should invalidate the catalog hash")
	}
	cfg.LegacyPath = filepath.Join(filepath.Dir(cfg.Root), "legacy.txt")
	write(t, cfg.LegacyPath, "OldClient")
	c, err := Load(context.Background(), cfg)
	if err != nil || b.Hash == c.Hash {
		t.Fatal("legacy gate changes should invalidate the catalog hash")
	}
}
