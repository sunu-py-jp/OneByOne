package rulepack

import (
	"bytes"
	"encoding/json"
	"fmt"

	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/solutionrules"
)

// Package entries share the internal editor model; portable solution documents
// always contain exactly id, description and content, never file-mode filters.
func FromSolutionRules(rules []solutionrules.Rule) (*Package, error) {
	p := &Package{Solution: true, Rules: []Entry{}}
	for _, r := range rules {
		if err := solutionrules.Validate([]solutionrules.Rule{r}); err != nil {
			return nil, err
		}
		md, err := ruleformat.Encode(model.RuleDefinition{ID: r.ID, Name: r.ID, Description: r.Description, Body: r.Content})
		if err != nil {
			return nil, err
		}
		p.Rules = append(p.Rules, Entry{ID: r.ID, Markdown: string(md)})
	}
	return p, nil
}

func SolutionRules(p *Package) ([]solutionrules.Rule, error) {
	result := []solutionrules.Rule{}
	for _, e := range p.Rules {
		d, err := ruleformat.Decode([]byte(e.Markdown))
		if err != nil {
			return nil, err
		}
		if d.PathPattern != "" || d.ContentPattern != "" || d.DiagnosticPattern != "" {
			return nil, fmt.Errorf("ソリューションルールは id・description・content の3項目だけです")
		}
		r := solutionrules.Rule{ID: d.ID, Description: d.Description, Content: d.Body}
		if err = solutionrules.Validate([]solutionrules.Rule{r}); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, nil
}

func isSolutionDocument(data []byte) bool {
	var d struct {
		Rules []json.RawMessage `json:"rules"`
	}
	if json.Unmarshal(bytes.TrimPrefix(data, utf8BOM), &d) != nil {
		return false
	}
	for _, r := range d.Rules {
		if b := bytes.TrimSpace(r); len(b) > 0 && b[0] == '{' {
			return true
		}
	}
	return false
}

func decodeSolutionDocument(data []byte) (*Package, error) {
	rules, err := solutionrules.Decode(data)
	if err != nil {
		return nil, err
	}
	return FromSolutionRules(rules)
}
func encodeSolutionDocument(p *Package) ([]byte, error) {
	if len(p.Rules) == 0 {
		return []byte("{\"rules\":[]}\n"), nil
	}
	rules, err := SolutionRules(p)
	if err != nil {
		return nil, err
	}
	return solutionrules.Encode(rules)
}
