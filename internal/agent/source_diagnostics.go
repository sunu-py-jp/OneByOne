package agent

import (
	"errors"
	"strings"
	"unicode/utf8"

	"onebyone/internal/model"
)

func validateSourceDiagnostics(file string, items []model.SourceDiagnostic) error {
	for _, item := range items {
		if item.File != file || item.Line < 0 || strings.TrimSpace(item.Message) == "" || !utf8.ValidString(item.Message) || !utf8.ValidString(item.Code) {
			return errors.New("Imported diagnostics must contain valid evidence for this target file only")
		}
	}
	return nil
}
