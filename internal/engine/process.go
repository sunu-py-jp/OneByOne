package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

type limitedBuffer struct {
	mu        sync.Mutex
	b         bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := b.max - b.b.Len()
	if n > left {
		b.truncated = true
	}
	if left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		b.b.Write(p)
	}
	return n, nil
}
func (b *limitedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func command(ctx context.Context, dir, executable string, args ...string) (string, error) {
	return runCommand(ctx, dir, executable, false, args...)
}

func runCommand(ctx context.Context, dir, executable string, requireComplete bool, args ...string) (string, error) {
	cmd, err := runtimeCommand(ctx, executable, args...)
	if err != nil {
		return "", err
	}
	cmd.Dir = dir
	cmd.WaitDelay = 3 * time.Second
	var out = limitedBuffer{max: 2 << 20}
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s: %w\n%s", executable, err, out.String())
	}
	if requireComplete && out.truncated {
		return "", fmt.Errorf("%s: output exceeds the 2MB limit; refusing an incomplete repository check", executable)
	}
	return out.String(), nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	base := []string{"--literal-pathspecs", "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "core.autocrlf=false", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "-c", "user.name=OneByOne", "-c", "user.email=onebyone@localhost"}
	return runCommand(ctx, dir, "git", true, append(base, args...)...)
}
