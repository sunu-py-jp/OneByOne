package rulepack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
)

func testConfig() model.Config {
	return model.Config{Root: "private-source", QueuePath: "private-queue", RulesPath: "original-rules", LegacyPath: "original-legacy", Provider: "openai", Endpoint: "https://private.example.invalid", Deployment: "private-model", AuthMode: "api_key", Credential: "FAKE-never-export-key", CredentialSet: true, RGPath: "custom-rg", IncludeGlobs: []string{"*.ts", "*.tsx"}, ExcludeGlobs: []string{"generated/**"}, CheckCommands: []model.Command{{Name: "check", Executable: "node", Args: []string{"scripts/check.js", "--verify"}}}, MaxAttempts: 2, MaxTurns: 9, MaxOutputTokens: 4096, MaxFileBytes: 32768, TimeoutSeconds: 90, MaxCostUSD: 1.5, InputPricePerMillion: 3, CachedInputPricePerMillion: 0.3, OutputPricePerMillion: 15}
}
func testDefinition(name, pattern string) []byte {
	data, err := ruleformat.Encode(model.RuleDefinition{Version: 1, Name: name, Overview: "Keep Legacy.Save behavior.", Before: "Legacy.Save()", After: "Modern.save()", Notes: "notes", HoldConditions: "hold", Pattern: pattern})
	if err != nil {
		panic(err)
	}
	return data
}
func testPackage() *Package {
	return &Package{Settings: FromConfig(testConfig()), Files: map[string][]byte{
		"rules/R001/rule.json":        testDefinition("Keep behavior", ""),
		"rules/R001/example.txt":      []byte("keep helper"),
		"rules/R019/rule.json":        testDefinition("Save", `Legacy\.Save`),
		"rules/R019/補助/コード例.txt":      []byte("Legacy.Save() → Modern.Save()\n"),
		"patterns/legacy-symbols.txt": []byte(`\bLegacy\b`),
	}}
}
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func manifestData(t *testing.T) []byte {
	t.Helper()
	data, err := validatePackage(testPackage())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type zipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func rawArchive(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		mode := entry.mode
		if mode == 0 {
			mode = 0600
		}
		header.SetMode(mode)
		out, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = out.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}
func baseEntries(t *testing.T) []zipEntry {
	t.Helper()
	return []zipEntry{{"package.json", manifestData(t), 0}, {"rules/R001/rule.json", testDefinition("valid", ""), 0}}
}

func TestRoundTripSnapshotSettingsAndUnicodeAssets(t *testing.T) {
	base := t.TempDir()
	expected := testPackage()
	for name, data := range expected.Files {
		writeFile(t, filepath.Join(base, filepath.FromSlash(name)), data)
	}
	writeFile(t, filepath.Join(base, "rules", ".DS_Store"), []byte("finder metadata"))
	p, err := Snapshot(filepath.Join(base, "rules"), filepath.Join(base, "patterns", "legacy-symbols.txt"), expected.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, expected) {
		t.Fatal("snapshot lost a rule asset or setting")
	}
	filename := filepath.Join(t.TempDir(), "比較用.oborules")
	if err = Write(filename, p); err != nil {
		t.Fatal(err)
	}
	actual, err := Read(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("archive roundtrip changed resource bytes or settings")
	}
	destination := t.TempDir()
	if err = Extract(actual, destination); err != nil {
		t.Fatal(err)
	}
	for name, data := range expected.Files {
		if !bytes.Equal(readFile(t, filepath.Join(destination, filepath.FromSlash(name))), data) {
			t.Fatalf("extracted resource differs: %s", name)
		}
	}
	metadata := readFile(t, filepath.Join(destination, "package.json"))
	for _, secret := range []string{"private-source", "private-queue", "original-rules", "original-legacy", "private.example.invalid", "private-model", "FAKE-never-export-key", `"provider"`, `"credential"`, `"queuePath"`, `"root"`} {
		if bytes.Contains(metadata, []byte(secret)) {
			t.Fatalf("package.json contains excluded data: %s", secret)
		}
	}
	config := testConfig()
	config.MaxAttempts, config.MaxTurns, config.MaxOutputTokens, config.MaxFileBytes, config.TimeoutSeconds = 1, 3, 2048, 65536, 180
	config.MaxCostUSD, config.InputPricePerMillion, config.CachedInputPricePerMillion, config.OutputPricePerMillion = 9, 7, 0.7, 21
	applied := actual.Settings.Apply(config)
	if !reflect.DeepEqual(applied, config) {
		t.Fatal("settings application changed excluded config fields")
	}
	applied.CheckCommands[0].Args[0] = "changed"
	applied.IncludeGlobs[0] = "changed"
	if actual.Settings.CheckCommands[0].Args[0] == "changed" || actual.Settings.IncludeGlobs[0] == "changed" {
		t.Fatal("settings share mutable arrays with workspace config")
	}
	if err = Extract(actual, destination); err == nil {
		t.Fatal("extraction overwrote an existing nonempty folder")
	}
}

func TestReadRejectsUnsafeAndAmbiguousArchivesWithoutWrites(t *testing.T) {
	cases := map[string][]zipEntry{}
	for _, name := range []string{"../../escaped.txt", "/absolute.txt", "C:/outside.txt", `rules\R001\outside.txt`, "./rules/R001/outside.txt", "rules//R001/outside.txt", "rules/R001/../outside.txt", "rules/R001/.env", "rules/R001/NUL.txt", "rules/R001/COM1.txt", "rules/R001/LPT¹.txt", "rules/R001/CONIN$", "rules/R001/colon:stream", "rules/R001/trailing.", "rules/R001/trailing ", "rules/R001/key.pem", "target-settings.json", "patterns/extra.txt", "rules/root.txt", "rules/R001/rule.md", "rules/R001/pattern.txt", "rules/R001/name.txt", "rules/R001/Rule.json"} {
		cases[name] = []zipEntry{{name, []byte("must never write"), 0}}
	}
	cases["duplicate"] = []zipEntry{{"rules/R001/rule.json", []byte("overwrite"), 0}}
	cases["case file"] = []zipEntry{{"rules/R001/Example.txt", nil, 0}, {"rules/R001/example.txt", nil, 0}}
	cases["case directory"] = []zipEntry{{"rules/r001/example.txt", nil, 0}}
	cases["unicode normalization"] = []zipEntry{{"rules/R001/café.txt", nil, 0}, {"rules/R001/cafe\u0301.txt", nil, 0}}
	cases["file as directory"] = []zipEntry{{"rules/R001/data", nil, 0}, {"rules/R001/data/example.txt", nil, 0}}
	cases["symlink"] = []zipEntry{{"rules/R001/link", []byte("../../outside"), os.ModeSymlink | 0777}}
	cases["device"] = []zipEntry{{"rules/R001/device", nil, os.ModeDevice | 0600}}
	cases["named pipe"] = []zipEntry{{"rules/R001/pipe", nil, os.ModeNamedPipe | 0600}}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			filename := filepath.Join(directory, "bad.oborules")
			writeFile(t, filename, rawArchive(t, append(baseEntries(t), extra...)))
			before, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Read(filename); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
			after, err := os.ReadDir(directory)
			if err != nil || len(after) != len(before) {
				t.Fatal("reading an unsafe archive wrote files")
			}
		})
	}
}

func TestManifestRejectsUnknownExcludedDuplicateAndUnsupportedFields(t *testing.T) {
	valid := string(manifestData(t))
	invalid := []string{strings.Replace(valid, `"version": 3`, `"version": 1`, 1), strings.Replace(valid, `"version": 3`, `"version": 3, "version": 3`, 1), strings.Replace(valid, `"settings": {`, `"settings": {"root":"outside",`, 1), strings.Replace(valid, `"settings": {`, `"settings": {"queuePath":"outside",`, 1), strings.Replace(valid, `"settings": {`, `"settings": {"provider":"openai",`, 1), strings.Replace(valid, `"settings": {`, `"settings": {"credential":"FAKE-key",`, 1), strings.Replace(valid, `"settings": {`, `"settings": {"connectionId":"personal",`, 1), strings.Replace(valid, `"settings": {`, `"unexpected":1,"settings": {`, 1), `{"version":3,"settings":null}`, `{"version":3}`, valid + ` {}`,
		strings.Replace(valid, `"version": 3`, `"version": 1, "VERSION": 3`, 1),
		strings.Replace(valid, `"includeGlobs":`, `"IncludeGlobs":`, 1),
		strings.Replace(valid, `"executable":`, `"Executable":`, 1),
		strings.Replace(valid, `"settings": {`, `"settings": {"rgPath":"untrusted-program",`, 1)}
	for _, key := range []string{"maxAttempts", "maxTurns", "maxOutputTokens", "maxFileBytes", "timeoutSeconds", "maxCostUSD", "inputPricePerMillion", "cachedInputPricePerMillion", "outputPricePerMillion"} {
		invalid = append(invalid, strings.Replace(valid, `"settings": {`, `"settings": {"`+key+`":1,`, 1))
	}
	for i, metadata := range invalid {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			entries := baseEntries(t)
			entries[0].data = []byte(metadata)
			filename := filepath.Join(t.TempDir(), "bad.oborules")
			writeFile(t, filename, rawArchive(t, entries))
			if _, err := Read(filename); err == nil {
				t.Fatal("invalid manifest was accepted")
			}
		})
	}
}

func TestReadReportsUnsupportedVersionBeforeOldSettings(t *testing.T) {
	entries := baseEntries(t)
	entries[0].data = []byte(`{"version":2,"settings":{"maxTurns":12,"includeGlobs":[],"excludeGlobs":[],"checkCommands":[]}}`)
	filename := filepath.Join(t.TempDir(), "version2.oborules")
	writeFile(t, filename, rawArchive(t, entries))
	if _, err := Read(filename); err == nil || !strings.Contains(err.Error(), "version 2") || !strings.Contains(err.Error(), "version 3") {
		t.Fatalf("old package did not report its unsupported version: %v", err)
	}
}

func TestReadRejectsCRCFailureAndTruncatedZip(t *testing.T) {
	entries := baseEntries(t)
	entries = append(entries, zipEntry{"rules/R001/example.txt", []byte("checksum-target-unique"), 0})
	body := rawArchive(t, entries)
	offset := bytes.Index(body, []byte("checksum-target-unique"))
	if offset < 0 {
		t.Fatal("test payload not present")
	}
	body[offset] ^= 1
	for name, data := range map[string][]byte{"crc": body, "truncated": body[:len(body)-10]} {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "bad.oborules")
			writeFile(t, filename, data)
			if _, err := Read(filename); err == nil {
				t.Fatal("corrupt archive was accepted")
			}
		})
	}
}

func TestReadAndWriteEnforceResourceQuotas(t *testing.T) {
	t.Run("single file", func(t *testing.T) {
		entries := append(baseEntries(t), zipEntry{"rules/R001/large.txt", bytes.Repeat([]byte("x"), int(MaxFileBytes+1)), 0})
		filename := filepath.Join(t.TempDir(), "large.oborules")
		writeFile(t, filename, rawArchive(t, entries))
		if _, err := Read(filename); err == nil {
			t.Fatal("oversized entry accepted")
		}
	})
	t.Run("expanded total", func(t *testing.T) {
		entries := baseEntries(t)
		data := bytes.Repeat([]byte("x"), int(MaxFileBytes))
		for i := 0; i < 8; i++ {
			entries = append(entries, zipEntry{fmt.Sprintf("rules/R001/%d.bin", i), data, 0})
		}
		filename := filepath.Join(t.TempDir(), "large.oborules")
		writeFile(t, filename, rawArchive(t, entries))
		if _, err := Read(filename); err == nil {
			t.Fatal("expanded total exceeded limit")
		}
	})
	t.Run("file count", func(t *testing.T) {
		entries := baseEntries(t)
		for i := 0; i < MaxFiles; i++ {
			entries = append(entries, zipEntry{fmt.Sprintf("rules/R001/%d.txt", i), nil, 0})
		}
		filename := filepath.Join(t.TempDir(), "large.oborules")
		writeFile(t, filename, rawArchive(t, entries))
		if _, err := Read(filename); err == nil {
			t.Fatal("excessive file count accepted")
		}
	})
	t.Run("archive size", func(t *testing.T) {
		filename := filepath.Join(t.TempDir(), "large.oborules")
		f, err := os.Create(filename)
		if err != nil {
			t.Fatal(err)
		}
		err = f.Truncate(MaxArchiveBytes + 1)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Read(filename); err == nil {
			t.Fatal("oversized archive accepted")
		}
	})
	t.Run("invalid snapshot no output", func(t *testing.T) {
		p := testPackage()
		p.Files["rules/R001/too-large"] = bytes.Repeat([]byte("x"), int(MaxFileBytes+1))
		filename := filepath.Join(t.TempDir(), "existing.oborules")
		writeFile(t, filename, []byte("original"))
		if err := Write(filename, p); err == nil {
			t.Fatal("invalid snapshot written")
		}
		if string(readFile(t, filename)) != "original" {
			t.Fatal("validation failure modified existing archive")
		}
		destination := t.TempDir()
		if err := Extract(p, destination); err == nil {
			t.Fatal("invalid snapshot extracted")
		}
		entries, _ := os.ReadDir(destination)
		if len(entries) != 0 {
			t.Fatal("validation failure left partial output")
		}
	})
}

func TestRequiredDefinitionsAndExtension(t *testing.T) {
	for _, missing := range []string{"rules/R001/rule.json"} {
		t.Run(missing, func(t *testing.T) {
			p := testPackage()
			delete(p.Files, missing)
			if err := Write(filepath.Join(t.TempDir(), "rules.oborules"), p); err == nil {
				t.Fatal("required definition missing")
			}
		})
	}
	p := testPackage()
	p.Files["rules/R001/rule.json"] = []byte{0xff}
	if err := Write(filepath.Join(t.TempDir(), "rules.oborules"), p); err == nil {
		t.Fatal("invalid UTF-8 definition accepted")
	}
	if err := Write(filepath.Join(t.TempDir(), "rules.zip"), testPackage()); err == nil {
		t.Fatal("wrong extension accepted")
	}
	if _, err := Read(filepath.Join(t.TempDir(), "rules.zip")); err == nil {
		t.Fatal("wrong extension accepted on read")
	}
}

func TestSnapshotAndDestinationRejectLinksAndCredentialJunk(t *testing.T) {
	for _, kind := range []string{"file link", "directory link", "ancestor link", "hidden key", "unexpected root file"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			p := testPackage()
			for name, data := range p.Files {
				writeFile(t, filepath.Join(base, filepath.FromSlash(name)), data)
			}
			rules := filepath.Join(base, "rules")
			outside := t.TempDir()
			writeFile(t, filepath.Join(outside, "secret.txt"), []byte("FAKE-unrelated-secret"))
			switch kind {
			case "file link":
				if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(rules, "R001", "link.txt")); err != nil {
					t.Skip(err)
				}
			case "directory link":
				if err := os.Symlink(outside, filepath.Join(rules, "R001", "linked")); err != nil {
					t.Skip(err)
				}
			case "ancestor link":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(base, alias); err != nil {
					t.Skip(err)
				}
				rules = filepath.Join(alias, "rules")
			case "hidden key":
				writeFile(t, filepath.Join(rules, "R001", ".env"), []byte("FAKE-key"))
			case "unexpected root file":
				writeFile(t, filepath.Join(rules, "credentials.json"), []byte("FAKE-key"))
			}
			if _, err := Snapshot(rules, "", p.Settings); err == nil {
				t.Fatal("unsafe snapshot source accepted")
			}
		})
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "victim.oborules")
	writeFile(t, target, []byte("unchanged"))
	link := filepath.Join(t.TempDir(), "link.oborules")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if err := Write(link, testPackage()); err == nil {
		t.Fatal("symlink output accepted")
	}
	if _, err := Read(link); err == nil {
		t.Fatal("symlink input accepted")
	}
	if string(readFile(t, target)) != "unchanged" {
		t.Fatal("symlink target modified")
	}
	linkDir := filepath.Join(t.TempDir(), "linkdir")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Skip(err)
	}
	if err := Extract(testPackage(), linkDir); err == nil {
		t.Fatal("symlink extraction root accepted")
	}
}

func TestArchiveContainsOnlyExplicitSettingsKeys(t *testing.T) {
	metadata := manifestData(t)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatal("unexpected manifest fields")
	}
	var settings map[string]any
	if err := json.Unmarshal(m["settings"], &settings); err != nil {
		t.Fatal(err)
	}
	if string(m["version"]) != "3" {
		t.Fatalf("wrong package version: %s", m["version"])
	}
	want := []string{"includeGlobs", "excludeGlobs", "checkCommands"}
	if len(settings) != len(want) {
		t.Fatalf("wrong settings count: %d", len(settings))
	}
	for _, key := range want {
		if _, ok := settings[key]; !ok {
			t.Fatalf("missing setting %s", key)
		}
	}
}

func TestExtractCopiesNoExecutablePermissions(t *testing.T) {
	entries := baseEntries(t)
	entries = append(entries, zipEntry{"rules/R001/helper.sh", []byte("exit 1\n"), 0755})
	filename := filepath.Join(t.TempDir(), "rules.oborules")
	writeFile(t, filename, rawArchive(t, entries))
	p, err := Read(filename)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err = Extract(p, dest); err != nil {
		t.Fatal(err)
	}
	if err = filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && info.Mode().Perm()&0111 != 0 {
			t.Fatal("archive executable permission was restored")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
