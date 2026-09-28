package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/japanese"
	"onebyone/internal/model"
)

func TestContentPatternMatchesJapaneseAcrossUTF8AndShiftJIS(t *testing.T) {
	cfg := fixture(t)
	updateRule(t, cfg, "R001", func(d *model.RuleDefinition) { d.ContentPattern = "不存在" })
	updateRule(t, cfg, "R019", func(d *model.RuleDefinition) { d.ContentPattern = `請求\.保存` })
	source := "請求.保存();\r\n"
	sjis, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cfg.Root, "utf8.js"), source)
	write(t, filepath.Join(cfg.Root, "other.js"), "different();\n")
	if err := os.WriteFile(filepath.Join(cfg.Root, "shiftjis.js"), sjis, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, scanned, excluded, err := catalog.Scan(context.Background(), cfg)
	if err != nil || scanned != 3 || excluded != 1 || len(tasks) != 2 {
		t.Fatalf("scan: %+v %d %d %v", tasks, scanned, excluded, err)
	}
	hash := sha256.Sum256(sjis)
	if tasks[0].File != "shiftjis.js" || tasks[0].InputHash != hex.EncodeToString(hash[:]) || tasks[0].Status != "pending" || len(tasks[0].Rules) != 1 || tasks[0].Rules[0] != "R019" {
		t.Fatalf("Shift_JIS target lost original-byte identity: %+v", tasks[0])
	}
}
