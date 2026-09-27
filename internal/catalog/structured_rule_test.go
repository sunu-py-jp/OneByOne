package catalog

import (
	"context"
	"errors"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
	"strings"
	"testing"
)

func TestFreeMarkdownAndDescriptionReachModel(t *testing.T) {
	cfg := fixture(t)
	d := model.RuleDefinition{Name: "free Markdown", Description: "explicit overview", Body: "# 任意の見出し\n\n- list\n\n```js\nbefore();\n```\n\n## nested"}
	writeRule(t, cfg, "R001", d)
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := c.ReadRule("R001")
	if body != ruleformat.Markdown(d) || !strings.Contains(body, d.Description) || !strings.Contains(body, d.Body) || !strings.Contains(c.SystemPrompt, body) {
		t.Fatal(body)
	}
	d.PathPattern = "src/**"
	writeRule(t, cfg, "R001", d)
	c, err = Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = c.ReadRule("R001")
	if strings.Contains(c.SystemPrompt, "before();") || strings.Contains(body, "src/**") {
		t.Fatal("individual body leaked to index or filters into body")
	}
}
func TestInvalidMarkdownRuleRemainsAddressable(t *testing.T) {
	cfg := fixture(t)
	p, err := rulepack.Snapshot(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	p.Rules[0].Markdown = "---\nid: R001\nname: missing-description\n---\nbody"
	saveRules(t, cfg, p)
	_, err = Load(context.Background(), cfg)
	var d *DiagnosticError
	if !errors.As(err, &d) || d.RuleID != "R001" {
		t.Fatal(err)
	}
}
