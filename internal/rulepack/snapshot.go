package rulepack

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Snapshot reads rule resources without following links, executing rg, or reading
// adjacent project files. Unexpected root files and hidden/credential assets are
// rejected; macOS's harmless .DS_Store metadata is omitted.
func Snapshot(rulesPath, legacyPath string, settings Settings) (*Package, error) {
	p := &Package{Settings: cloneSettings(settings), Files: map[string][]byte{}}
	if err := settings.validate(); err != nil {
		return nil, err
	}
	root, err := openDirectory(rulesPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	scanner := snapshotReader{packageData: p, index: pathIndex{}}
	if err := scanner.directory(root, "rules"); err != nil {
		return nil, err
	}
	if legacyPath != "" {
		full, err := checkedAbsolute(legacyPath)
		if err != nil {
			return nil, err
		}
		parent, err := openDirectory(filepath.Dir(full))
		if err != nil {
			return nil, err
		}
		err = scanner.file(parent, filepath.Base(full), "patterns/legacy-symbols.txt")
		parent.Close()
		if err != nil {
			return nil, err
		}
	}
	if _, err := validatePackage(p); err != nil {
		return nil, err
	}
	return p, nil
}

type snapshotReader struct {
	packageData *Package
	index       pathIndex
	entries     int
	total       int64
}

func (s *snapshotReader) directory(root *os.Root, prefix string) error {
	folder, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := folder.ReadDir(MaxFiles + 1 - s.entries)
	folder.Close()
	if err != nil && err != io.EOF {
		return err
	}
	s.entries += len(entries)
	if s.entries > MaxFiles {
		return fmt.Errorf("ルールフォルダの項目数が%d件を超えています", MaxFiles)
	}
	for _, entry := range entries {
		info, err := root.Lstat(entry.Name())
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("ルール資材にリンクや特殊ファイルは含められません: %s", entry.Name())
		}
		if entry.Name() == ".DS_Store" && info.Mode().IsRegular() {
			continue
		}
		name := prefix + "/" + entry.Name()
		if err := allowedPath(name, info.IsDir()); err != nil {
			return err
		}
		if err := s.index.add(name, info.IsDir()); err != nil {
			return err
		}
		if info.IsDir() {
			child, err := root.OpenRoot(entry.Name())
			if err != nil {
				return err
			}
			after, err := child.Stat(".")
			current, currentErr := root.Lstat(entry.Name())
			if err != nil || currentErr != nil || !current.IsDir() || !os.SameFile(info, after) || !os.SameFile(after, current) {
				child.Close()
				return fmt.Errorf("ルールフォルダが読み込み中に変更されました")
			}
			err = s.directory(child, name)
			child.Close()
			if err != nil {
				return err
			}
		} else if err := s.file(root, entry.Name(), name); err != nil {
			return err
		}
	}
	return nil
}
func (s *snapshotReader) file(root *os.Root, input, name string) error {
	if len(s.packageData.Files)+1 >= MaxFiles {
		return fmt.Errorf("ルールファイル数が上限を超えています")
	}
	f, err := openRegular(root, input)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > MaxFileBytes || info.Size() > MaxExpandedBytes-s.total {
		return fmt.Errorf("ルール資材のサイズが上限を超えています: %s", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > MaxFileBytes {
		return fmt.Errorf("ルール資材が4 MiBを超えています: %s", name)
	}
	s.total += int64(len(data))
	if s.total > MaxExpandedBytes {
		return fmt.Errorf("ルール資材の合計が32 MiBを超えています")
	}
	s.packageData.Files[name] = data
	return nil
}
