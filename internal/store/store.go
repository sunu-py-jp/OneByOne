package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"onebyone/internal/model"
)

// WriteFile publishes a complete, flushed snapshot; incomplete temporary files
// are never treated as committed state.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".onebyone-write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = replaceFile(tmp, path); err != nil {
		return err
	}
	if d, e := os.Open(filepath.Dir(path)); e == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func WriteJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, append(b, '\n'), 0600)
}

// ReadFile reads an app-managed snapshot. On Windows the reader permits an
// atomic replacement, while its open handle continues to read the old contents.
func ReadFile(path string) ([]byte, error) {
	f, err := openSnapshot(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func SaveQueue(path string, tasks []model.Task) error {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, t := range tasks {
		if err := enc.Encode(t); err != nil {
			return err
		}
	}
	return WriteFile(path, []byte(b.String()), 0600)
}

func LoadQueue(path string) ([]model.Task, error) {
	f, err := openSnapshot(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, e := f.Stat(); e != nil {
		return nil, e
	} else if info.Size() > 256<<20 {
		return nil, fmt.Errorf("キューが256MBの上限を超えています")
	}
	r := bufio.NewScanner(io.LimitReader(f, 256<<20))
	r.Buffer(make([]byte, 64<<10), 8<<20)
	tasks := []model.Task{}
	seen := map[string]bool{}
	seenAttempts := map[string]bool{}
	line := 0
	for r.Scan() {
		line++
		text := strings.TrimSpace(strings.TrimPrefix(r.Text(), "\ufeff"))
		if text == "" {
			continue
		}
		var t model.Task
		if err := json.Unmarshal([]byte(text), &t); err != nil {
			return nil, fmt.Errorf("JSONL %d行: %w", line, err)
		}
		if t.File == "" || seen[strings.ToLower(t.File)] {
			return nil, fmt.Errorf("JSONL %d行: ファイル名が空か重複しています", line)
		}
		seen[strings.ToLower(t.File)] = true
		if t.Status == "" {
			t.Status = "pending"
		}
		switch t.Status {
		case "pending", "running", "done", "skipped", "failed", "needs_human":
		default:
			return nil, fmt.Errorf("JSONL %d行: 不明な状態 %s", line, t.Status)
		}
		if t.Attempts < 0 {
			return nil, fmt.Errorf("JSONL %d行: attempts は0以上です", line)
		}
		for _, h := range t.History {
			if !validID(h.ID) || seenAttempts[h.ID] {
				return nil, fmt.Errorf("JSONL %d行: 不正な試行IDです", line)
			}
			seenAttempts[h.ID] = true
		}
		if t.Rules == nil {
			t.Rules = []string{}
		}
		if t.History == nil {
			t.History = []model.Attempt{}
		}
		if t.RulesApplied == nil {
			t.RulesApplied = []string{}
		}
		tasks = append(tasks, t)
	}
	if err := r.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

// Lock uses an OS-held advisory lock. The file remains in place so concurrent
// processes always lock the same inode; process termination releases the lock.
func Lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("別の実行がこのキューを使用中です: %w", err)
	}
	if err = f.Truncate(0); err == nil {
		_, err = fmt.Fprintf(f, "%d\n%d\n", os.Getpid(), time.Now().Unix())
	}
	if err != nil {
		unlockFile(f)
		f.Close()
		return nil, err
	}
	_ = f.Sync()
	return func() { unlockFile(f); _ = f.Close() }, nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
