package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func fixture(t *testing.T) model.Config {
	t.Helper()
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("rg required")
	}
	dir := t.TempDir()
	cfg := model.Config{Root: filepath.Join(dir, "source"), RulesPath: filepath.Join(dir, "rules", "rules.json"), RGPath: rg}
	if err = os.MkdirAll(cfg.Root, 0755); err != nil {
		t.Fatal(err)
	}
	writeRule(t, cfg, "R001", model.RuleDefinition{Name: "共通ルール", Description: "既存の動作を保持します。", Body: "自由本文\n\n# 補足\n\n保持する。"})
	writeRule(t, cfg, "R019", model.RuleDefinition{Name: "保存処理の変更", Description: "OldClient.Save を NewClient.Persist に移行する", Body: "# 変換前\n\nindividual before example", ContentPattern: `\.Save\s*\(`})
	return cfg
}
func saveRules(t *testing.T, cfg model.Config, p *rulepack.Package) {
	t.Helper()
	docs := []string{}
	for _, e := range p.Rules {
		docs = append(docs, e.Markdown)
	}
	b, _ := json.Marshal(map[string]any{"rules": docs})
	write(t, cfg.RulesPath, string(b))
}
func writeRule(t *testing.T, cfg model.Config, id string, d model.RuleDefinition) {
	t.Helper()
	d.ID = id
	b, err := ruleformat.Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	p, err := rulepack.Snapshot(cfg.RulesPath)
	if err != nil {
		p = &rulepack.Package{}
	}
	for i, e := range p.Rules {
		if e.ID == id {
			p.Rules[i].Markdown = string(b)
			saveRules(t, cfg, p)
			return
		}
	}
	p.Rules = append(p.Rules, rulepack.Entry{ID: id, Markdown: string(b)})
	saveRules(t, cfg, p)
}
func updateRule(t *testing.T, cfg model.Config, id string, fn func(*model.RuleDefinition)) {
	t.Helper()
	p, err := rulepack.Snapshot(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p.Rules {
		if e.ID == id {
			d, err := ruleformat.Decode([]byte(e.Markdown))
			if err != nil {
				t.Fatal(err)
			}
			fn(&d)
			writeRule(t, cfg, id, d)
			return
		}
	}
	t.Fatal("rule missing")
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
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Rules[0].Always || c.Rules[1].Always || !strings.Contains(c.SystemPrompt, "OldClient.Save") || strings.Contains(c.SystemPrompt, "individual before example") {
		t.Fatal("wrong prompt", c.SystemPrompt)
	}
	tasks, scanned, excluded, err := c.Scan(context.Background(), cfg)
	if err != nil || scanned != 2 || excluded != 0 || len(tasks) != 2 {
		t.Fatal(tasks, scanned, excluded, err)
	}
	if !reflect.DeepEqual(tasks[0].Rules, []string{"R001", "R019"}) || !reflect.DeepEqual(tasks[1].Rules, []string{"R001"}) {
		t.Fatal("wrong scoped IDs", tasks)
	}
	hash := sha256.Sum256([]byte("OldClient.Save (options);\r\n"))
	if tasks[0].InputHash != hex.EncodeToString(hash[:]) {
		t.Fatal("source hash modified")
	}
	scoped, err := c.ForRules(tasks[1].Rules)
	if err != nil || len(scoped.Rules) != 1 {
		t.Fatal(err)
	}
	if _, err = scoped.ReadRule("R019"); err == nil {
		t.Fatal("out-of-scope read allowed")
	}
	if _, err = c.ForRules([]string{"missing"}); err == nil {
		t.Fatal("missing rule allowed")
	}
	old, _ := c.ReadRule("R001")
	updateRule(t, cfg, "R001", func(d *model.RuleDefinition) { d.Body = "new body" })
	actual, _ := c.ReadRule("R001")
	if old != actual {
		t.Fatal("run snapshot changed")
	}
	changed, err := Load(context.Background(), cfg)
	if err != nil || changed.Hash == c.Hash {
		t.Fatal("rule change undetected", err)
	}
}
func TestInvalidPatternsTargetSpecificRule(t *testing.T) {
	for _, pattern := range []string{"(", "["} {
		t.Run(pattern, func(t *testing.T) {
			cfg := fixture(t)
			updateRule(t, cfg, "R019", func(d *model.RuleDefinition) { d.ContentPattern = pattern })
			_, err := Load(context.Background(), cfg)
			var diagnostic *DiagnosticError
			if !errors.As(err, &diagnostic) || diagnostic.RuleID != "R019" {
				t.Fatal("missing rule diagnostic", err)
			}
		})
	}
}
func TestInvalidGlobFailsBeforeScan(t *testing.T) {
	for _, pattern := range []string{"[", "../**", "/src/**", `src\*.tsx`, "!src/**"} {
		t.Run(pattern, func(t *testing.T) {
			cfg := fixture(t)
			updateRule(t, cfg, "R019", func(d *model.RuleDefinition) { d.PathPattern = pattern })
			if _, err := Load(context.Background(), cfg); err == nil {
				t.Fatal("invalid glob accepted")
			}
		})
	}
}
func TestScopeExclusionsAndIgnoreIndependence(t *testing.T) {
	cfg := fixture(t)
	for _, id := range []string{"R001", "R019"} {
		updateRule(t, cfg, id, func(d *model.RuleDefinition) { d.PathPattern = "src/**" })
	}
	for _, f := range []string{"src/a.ext", "src/ignored.ext", "node_modules/a.ext", "src/node_modules/a.ext", "src/.env", "src/AGENTS.md", "src/Agents.md", "src/key.pem"} {
		write(t, filepath.Join(cfg.Root, f), "OldClient.Save()")
	}
	write(t, filepath.Join(cfg.Root, ".gitignore"), "src/ignored.ext")
	rc := filepath.Join(t.TempDir(), "rg.conf")
	write(t, rc, "--glob=!src/**")
	t.Setenv("RIPGREP_CONFIG_PATH", rc)
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, _, err := c.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 2 || tasks[0].File != "src/a.ext" || tasks[1].File != "src/ignored.ext" {
		t.Fatal(tasks, err)
	}
}
func TestUnsupportedSourcesRemainVisible(t *testing.T) {
	cfg := fixture(t)
	write(t, filepath.Join(cfg.Root, "binary.ext"), "OldClient\x00")
	write(t, filepath.Join(cfg.Root, "encoding.ext"), "OldClient\xff")
	write(t, filepath.Join(cfg.Root, "large.ext"), strings.Repeat("a", 2048))
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, _, err := c.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 3 {
		t.Fatal(tasks, err)
	}
	for _, task := range tasks {
		if (task.File != "large.ext" && task.Status != "needs_human") || (task.File == "large.ext" && task.Status != "pending") || len(task.InputHash) != 64 {
			t.Fatal(task)
		}
	}
	hash := sha256.Sum256([]byte(strings.Repeat("a", 2048)))
	if tasks[2].InputHash != hex.EncodeToString(hash[:]) {
		t.Fatal("large file hash truncated")
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
	if err := os.Rename(cfg.RulesPath, cfg.RulesPath+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cfg.RulesPath+".original", cfg.RulesPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), cfg); err == nil {
		t.Fatal("symlinked rules.json accepted")
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
