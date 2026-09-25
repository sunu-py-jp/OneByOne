package rulepack

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"onebyone/internal/ruleformat"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxArchiveBytes  int64 = 64 << 20
	MaxExpandedBytes int64 = 32 << 20
	MaxFileBytes     int64 = 4 << 20
	MaxFiles               = 5000
	maxManifestBytes int64 = 1 << 20
)

// Files contains only rules/... and optional patterns/legacy-symbols.txt.
// package.json is generated from Settings rather than accepted as an asset.
type Package struct {
	Settings Settings
	Files    map[string][]byte
}
type manifest struct {
	Version  int       `json:"version"`
	Settings *Settings `json:"settings"`
}

func archiveFilename(name string) error {
	if !strings.EqualFold(filepath.Ext(name), ".oborules") {
		return fmt.Errorf("拡張子 .oborules のファイルを指定してください")
	}
	return nil
}

// Read completely validates the archive, including every file's CRC and expanded
// byte quotas, before returning a snapshot. It makes no filesystem writes.
func Read(filename string) (*Package, error) {
	if err := archiveFilename(filename); err != nil {
		return nil, err
	}
	full, err := checkedAbsolute(filename)
	if err != nil {
		return nil, err
	}
	root, err := openDirectory(filepath.Dir(full))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := openRegular(root, filepath.Base(full))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxArchiveBytes {
		return nil, fmt.Errorf("ルールパッケージが64 MiBを超えています")
	}
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return nil, fmt.Errorf("ZIP形式のルールパッケージを読み込めません: %w", err)
	}
	if len(reader.File) > MaxFiles {
		return nil, fmt.Errorf("ルールパッケージの項目数が%d件を超えています", MaxFiles)
	}
	p := &Package{Files: map[string][]byte{}}
	index := pathIndex{}
	seen := map[string]bool{}
	var total int64
	var metadata []byte
	for _, entry := range reader.File {
		directory := entry.FileInfo().IsDir()
		name := entry.Name
		if directory {
			name = strings.TrimSuffix(name, "/")
		}
		if seen[name] {
			return nil, fmt.Errorf("パッケージ内のパスが重複しています: %q", name)
		}
		seen[name] = true
		if err := allowedPath(name, directory); err != nil {
			return nil, err
		}
		if err := index.add(name, directory); err != nil {
			return nil, err
		}
		if entry.Flags&1 != 0 || (!directory && !entry.Mode().IsRegular()) || entry.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("暗号化ZIP・リンク・特殊ファイルは利用できません: %q", name)
		}
		if directory {
			if entry.UncompressedSize64 != 0 || entry.CompressedSize64 != 0 {
				return nil, fmt.Errorf("フォルダにデータが含まれています")
			}
			continue
		}
		limit := MaxFileBytes
		if name == "package.json" {
			limit = maxManifestBytes
		}
		if entry.UncompressedSize64 > uint64(limit) || entry.UncompressedSize64 > uint64(MaxExpandedBytes-total) {
			return nil, fmt.Errorf("ルールパッケージの展開サイズが上限を超えています")
		}
		stream, err := entry.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, limit+1))
		closeErr := stream.Close()
		if readErr != nil {
			return nil, fmt.Errorf("パッケージ内ファイルを検証できません: %q: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if int64(len(data)) > limit || uint64(len(data)) != entry.UncompressedSize64 {
			return nil, fmt.Errorf("パッケージ内ファイルのサイズが不正です: %q", name)
		}
		total += int64(len(data))
		if total > MaxExpandedBytes {
			return nil, fmt.Errorf("ルールパッケージの合計展開サイズが32 MiBを超えています")
		}
		if name == "package.json" {
			metadata = data
		} else {
			p.Files[name] = data
		}
	}
	if metadata == nil {
		return nil, fmt.Errorf("package.json がありません")
	}
	// Inspect the version before decoding version-specific settings so an old
	// archive reports its unsupported format, rather than an arbitrary old key.
	var header struct {
		Version  int             `json:"version"`
		Settings json.RawMessage `json:"settings"`
	}
	if err := strictJSON(metadata, &header); err != nil {
		return nil, fmt.Errorf("package.json が不正です: %w", err)
	}
	if header.Version != 3 {
		return nil, fmt.Errorf("ルールパッケージのversion %dは使用できません。対応形式はversion 3です（処理上限・料金設定はワークスペースで管理します）", header.Version)
	}
	var m manifest
	if err := strictJSON(metadata, &m); err != nil {
		return nil, fmt.Errorf("package.json が不正です: %w", err)
	}
	if m.Settings == nil {
		return nil, fmt.Errorf("package.json のsettingsは必須です")
	}
	p.Settings = cloneSettings(*m.Settings)
	if _, err := validatePackage(p); err != nil {
		return nil, err
	}
	return p, nil
}

func validatePackage(p *Package) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("ルールパッケージがありません")
	}
	if err := p.Settings.validate(); err != nil {
		return nil, err
	}
	settings := cloneSettings(p.Settings)
	metadata, err := json.MarshalIndent(manifest{3, &settings}, "", "  ")
	if err != nil {
		return nil, err
	}
	metadata = append(metadata, '\n')
	if int64(len(metadata)) > maxManifestBytes {
		return nil, fmt.Errorf("package.json が1 MiBを超えています")
	}
	if len(p.Files)+1 > MaxFiles {
		return nil, fmt.Errorf("ルールパッケージのファイル数が上限を超えています")
	}
	index := pathIndex{}
	_ = index.add("package.json", false)
	total := int64(len(metadata))
	rules := map[string]bool{}
	for _, name := range sortedFiles(p.Files) {
		data := p.Files[name]
		if name == "package.json" {
			return nil, fmt.Errorf("package.json はsettingsから生成します")
		}
		if err := allowedPath(name, false); err != nil {
			return nil, err
		}
		if err := index.add(name, false); err != nil {
			return nil, err
		}
		if int64(len(data)) > MaxFileBytes {
			return nil, fmt.Errorf("パッケージ内ファイルが4 MiBを超えています: %q", name)
		}
		total += int64(len(data))
		if total > MaxExpandedBytes {
			return nil, fmt.Errorf("ルールパッケージの合計展開サイズが32 MiBを超えています")
		}
		parts := strings.Split(name, "/")
		if parts[0] == "rules" {
			rules[parts[1]] = true
		}
		if len(parts) == 3 && parts[2] == "rule.json" {
			if _, err := ruleformat.Decode(data); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		}
		definition := name == "patterns/legacy-symbols.txt"
		if definition {
			if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
				return nil, fmt.Errorf("ルール定義はUTF-8テキストで指定してください: %q", name)
			}
			if strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff")) == "" {
				return nil, fmt.Errorf("ルール定義が空です: %q", name)
			}
		}
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("rules/<ID>/rule.json が必要です")
	}
	for id := range rules {
		for _, file := range []string{"rule.json"} {
			if _, ok := p.Files["rules/"+id+"/"+file]; !ok {
				return nil, fmt.Errorf("rules/%s/%s がありません", id, file)
			}
		}
	}
	return metadata, nil
}

func sortedFiles(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Write validates first, then atomically replaces one .oborules file. The caller
// chooses the destination explicitly; this function never creates its parents.
func Write(filename string, p *Package) error {
	if err := archiveFilename(filename); err != nil {
		return err
	}
	metadata, err := validatePackage(p)
	if err != nil {
		return err
	}
	full, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	root, err := openDirectory(filepath.Dir(full))
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(full)
	if err := portablePath(name); err != nil {
		return err
	}
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("通常ファイル以外に保存できません")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	temporary := ".oborules-write-" + hex.EncodeToString(random)
	f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	archive := zip.NewWriter(f)
	for _, entry := range append([]string{"package.json"}, sortedFiles(p.Files)...) {
		header := &zip.FileHeader{Name: entry, Method: zip.Deflate}
		header.SetMode(0600)
		out, e := archive.CreateHeader(header)
		if e != nil {
			err = e
			break
		}
		data := p.Files[entry]
		if entry == "package.json" {
			data = metadata
		}
		if _, err = out.Write(data); err != nil {
			break
		}
	}
	closeZIP := archive.Close()
	if err == nil {
		err = closeZIP
	}
	if err == nil {
		err = f.Sync()
	}
	closeFile := f.Close()
	if err == nil {
		err = closeFile
	}
	if err != nil {
		return err
	}
	// Recheck leaf type before replacement. Rename replaces a leaf symlink rather
	// than following it, and the rooted parent prevents path escapes.
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("保存先が変更されました")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = root.Rename(temporary, name); err != nil {
		return err
	}
	if dir, err := root.Open("."); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// Extract accepts only a caller-owned existing empty directory. Invalid packages
// cause no writes. On an I/O failure, only entries this call created are removed.
func Extract(p *Package, destination string) (err error) {
	metadata, err := validatePackage(p)
	if err != nil {
		return err
	}
	root, err := openDirectory(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.Readdirnames(1)
	directory.Close()
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("展開先は空の専用フォルダを指定してください")
	}
	created := map[string]bool{}
	defer func() {
		if err != nil {
			for name := range created {
				_ = root.RemoveAll(name)
			}
		}
	}()
	for _, name := range append([]string{"package.json"}, sortedFiles(p.Files)...) {
		top := strings.Split(name, "/")[0]
		if strings.Contains(name, "/") && !created[top] {
			if err = root.Mkdir(top, 0700); err != nil {
				return err
			}
			created[top] = true
		}
		parent := path.Dir(name)
		if parent != "." {
			if err = root.MkdirAll(filepath.FromSlash(parent), 0700); err != nil {
				return err
			}
		}
		var file *os.File
		file, err = root.OpenFile(filepath.FromSlash(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if !strings.Contains(name, "/") {
			created[top] = true
		}
		data := p.Files[name]
		if name == "package.json" {
			data = metadata
		}
		_, err = file.Write(data)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Reject duplicate JSON keys as well as unknown fields, trailing data and
// unsupported versions; standard decoding alone accepts repeated key overrides.
func strictJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := uniqueJSON(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("JSON末尾に余分なデータがあります")
	}
	if err := exactJSONKeys(data, reflect.TypeOf(value)); err != nil {
		return err
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

// encoding/json accepts case-insensitive aliases for tagged fields. Package
// manifests use exact keys so VERSION cannot override version, and an unknown
// spelling cannot silently change how another implementation reads the archive.
func exactJSONKeys(data []byte, kind reflect.Type) error {
	if kind == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	switch kind.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < kind.NumField(); i++ {
			field := kind.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
		}
		for name, raw := range object {
			field, known := fields[name]
			if !known {
				return fmt.Errorf("未定義のJSONキーです: %s", name)
			}
			if err := exactJSONKeys(raw, field); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, raw := range values {
			if err := exactJSONKeys(raw, kind.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
func uniqueJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("JSONキーが重複しています")
			}
			seen[name] = true
			if err := uniqueJSON(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := uniqueJSON(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("JSON構造が不正です")
	}
	_, err = decoder.Token()
	return err
}
