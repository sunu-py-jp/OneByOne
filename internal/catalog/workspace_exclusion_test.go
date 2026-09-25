package catalog

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// App settings live outside the source tree. A project can legitimately use
// OneByOne as an ordinary namespace/directory and must not disappear from scope.
func TestOneByOneSourceDirectoriesAreNotReserved(t *testing.T) {
	cfg := fixture(t)
	cfg.IncludeGlobs = []string{"*.go"}
	cfg.ExcludeGlobs = []string{"OneByOne/generated/**"}
	for _, name := range []string{"OneByOne/client.go", "OneByOne/generated/skip.go", "src/oNeByOnE/service.go", "OneByOne.go", "src/real.go"} {
		write(t, filepath.Join(cfg.Root, filepath.FromSlash(name)), "OldClient.Save();\n")
	}
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, scanned, excluded, err := c.Scan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, task := range tasks {
		files = append(files, task.File)
	}
	if scanned != 4 || excluded != 0 || !reflect.DeepEqual(files, []string{"OneByOne.go", "OneByOne/client.go", "src/oNeByOnE/service.go", "src/real.go"}) {
		t.Fatalf("ordinary source directories were excluded: %v scanned=%d excluded=%d", files, scanned, excluded)
	}
	for _, name := range []string{"OneByOne/client.go", `src\oNeByOnE\service.go`, "OneByOne.go"} {
		if _, err := PathWithin(cfg.Root, name); err != nil {
			t.Fatalf("ordinary source access rejected %s: %v", name, err)
		}
	}
}
