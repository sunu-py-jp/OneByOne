package rulepack

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"path/filepath"
	"strings"
)

// WriteCSVTemplate writes an empty import template that Excel can open as UTF-8.
// Only headers are included so importing an unfilled template creates no rules.
func WriteCSVTemplate(path string) error {
	if !strings.EqualFold(filepath.Ext(path), ".csv") {
		return fmt.Errorf("保存先はCSVファイルを指定してください")
	}
	var data bytes.Buffer
	data.Write(utf8BOM)
	w := csv.NewWriter(&data)
	w.UseCRLF = true
	if err := w.Write([]string{
		"id", "name", "description", "path_pattern", "content_pattern", "diagnostic_pattern",
		"変更概要", "変換前", "変換後", "補足", "変換を保留にすべきケース", "修正後に残っていてはいけないパターン",
	}); err != nil {
		return err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return writeRegularFile(path, data.Bytes())
}
