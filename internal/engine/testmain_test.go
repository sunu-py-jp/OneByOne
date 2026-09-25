package engine

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	runBundledGitFixture()
	dir, err := os.MkdirTemp("", "onebyone-engine-private-tests-")
	if err != nil {
		panic(err)
	}
	if err = os.Setenv("ONEBYONE_PRIVATE_DIR", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
