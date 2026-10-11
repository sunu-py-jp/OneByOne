package rulepack

import (
	"strings"
	"testing"

	"onebyone/internal/ruleformat"
)

func TestCSVOptionalDiagnosticPatternMetadata(t *testing.T) {
	for _, test := range []struct {
		name, header, row, want string
	}{
		{"old CSV", "id,name,description,path_pattern,content_pattern,補足", "R1,rule,description,,,body", ""},
		{"diagnostic CSV", "diagnostic_pattern,id,name,description,path_pattern,content_pattern,補足", "^CS1061\\b,R1,rule,description,,,body", `^CS1061\b`},
		{"empty diagnostic", "id,name,description,path_pattern,content_pattern,diagnostic_pattern,補足", "R1,rule,description,,,,body", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			pkg, err := DecodeCSV([]byte(test.header + "\n" + test.row + "\n"))
			if err != nil || len(pkg.Rules) != 1 {
				t.Fatalf("decode: %+v %v", pkg, err)
			}
			rule, err := ruleformat.Decode([]byte(pkg.Rules[0].Markdown))
			if err != nil || rule.DiagnosticPattern != test.want || rule.Body != "# 補足\n\nbody" {
				t.Fatalf("metadata became body or was lost: %+v %v", rule, err)
			}
			if test.want == "" && strings.Contains(pkg.Rules[0].Markdown, "diagnostic_pattern:") {
				t.Fatal("empty optional field changed legacy Markdown")
			}
		})
	}
	if _, err := DecodeCSV([]byte("id,name,description,path_pattern,content_pattern,diagnostic_pattern\nR1,rule,description,,,[\n")); err == nil {
		t.Fatal("invalid diagnostic regex accepted from CSV")
	}
}
