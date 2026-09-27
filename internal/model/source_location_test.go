package model

import "testing"

func TestValidateSourceLocationUsesExactDeclaredFullLines(t *testing.T) {
	source := "\ufefffunction save() {\r\n  send();\r\n  send();\r\n}\r\n"
	tests := []struct {
		name     string
		location SourceLocation
		valid    bool
	}{
		{"single", SourceLocation{2, 2, "  send();"}, true},
		{"same-text-other-real-line", SourceLocation{3, 3, "  send();\n"}, true},
		{"multiline-crlf", SourceLocation{2, 3, "  send();\r\n  send();\r\n"}, true},
		{"no-indent", SourceLocation{2, 2, "send();"}, false},
		{"wrong-line-with-existing-text", SourceLocation{1, 1, "  send();"}, false},
		{"wrong-end", SourceLocation{2, 3, "  send();"}, false},
		{"outside", SourceLocation{5, 5, "  send();"}, false},
		{"zero", SourceLocation{0, 1, "function save() {"}, false},
		{"reversed", SourceLocation{3, 2, "  send();"}, false},
		{"empty", SourceLocation{2, 2, ""}, false},
		{"fabricated", SourceLocation{2, 2, "  flush();"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateSourceLocation(source, test.location); (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
	if err := ValidateSourceLocation("", SourceLocation{1, 1, "send();"}); err == nil {
		t.Fatal("empty source accepted")
	}
	if err := ValidateSourceLocation("send();", SourceLocation{1, 1, "send();"}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSourceLocationNormalizesOnlyUniqueFullLineEvidence(t *testing.T) {
	source := "first();\n  send();\nlast();\n"
	resolved, err := ResolveSourceLocation(source, SourceLocation{StartLine: 30, EndLine: 31, Excerpt: "  send();\n"})
	if err != nil || resolved.StartLine != 2 || resolved.EndLine != 2 {
		t.Fatalf("unique source was not corrected: %+v %v", resolved, err)
	}
	if err = ValidateSourceLocation(source, SourceLocation{StartLine: 30, EndLine: 31, Excerpt: "  send();\n"}); err == nil {
		t.Fatal("strict archive validator relocated old evidence")
	}
	if _, err = ResolveSourceLocation(source, SourceLocation{StartLine: 30, EndLine: 31, Excerpt: "send();"}); err == nil {
		t.Fatal("partial-line/indent mismatch was resolved")
	}
	repeated := "  send();\nother();\n  send();\n"
	if _, err = ResolveSourceLocation(repeated, SourceLocation{StartLine: 2, EndLine: 2, Excerpt: "  send();"}); err == nil {
		t.Fatal("ambiguous excerpt relocated")
	}
	resolved, err = ResolveSourceLocation(repeated, SourceLocation{StartLine: 3, EndLine: 3, Excerpt: "  send();"})
	if err != nil || resolved.StartLine != 3 {
		t.Fatal("explicit correct repeated location was rejected", err)
	}
}
