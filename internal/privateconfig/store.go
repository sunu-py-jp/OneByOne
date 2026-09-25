// Package privateconfig stores LLM connection settings separately from shared
// workspaces. Its directory must be selected from the current user's local app
// data, never from the source tree or a shared workspace directory.
package privateconfig

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	formatVersion   = 1
	profileDir      = "llm-settings"
	keyFile         = "master-key.json"
	maxProfileBytes = 1 << 20
	maxKeyBytes     = 16 << 10
)

var (
	ErrInvalidStore     = errors.New("個人用LLM設定の保存先または暗号化キーを確認できません")
	ErrInvalidSettings  = errors.New("個人用LLM設定が破損しているか、このユーザー・ワークスペースでは復号できません")
	ErrInvalidWorkspace = errors.New("個人用LLM設定のワークスペースIDが不正です")
	ErrWrite            = errors.New("個人用LLM設定を保存できません")
)

// LLMSettings is intentionally independent of shared configuration models.
// Every field, including endpoints and credentials, is encrypted on disk.
type LLMSettings struct {
	Provider       string `json:"provider"`
	Endpoint       string `json:"endpoint"`
	Deployment     string `json:"deployment"`
	AuthMode       string `json:"authMode"`
	Credential     string `json:"credential"`
	OAuthTenantID  string `json:"oauthTenantId,omitempty"`
	OAuthClientID  string `json:"oauthClientId,omitempty"`
	OAuthUsername  string `json:"oauthUsername,omitempty"`
	OAuthAccountID string `json:"oauthAccountId,omitempty"`
	OAuthCache     []byte `json:"oauthCache,omitempty"`
	// Rotated on login/logout/identity changes, not on silent token renewal.
	OAuthGeneration string `json:"oauthGeneration,omitempty"`
}

type Store struct {
	dir string
	key []byte
}

type keyEnvelope struct {
	Version    int    `json:"version"`
	Protection string `json:"protection"`
	Key        []byte `json:"key"`
}

type profileEnvelope struct {
	Version    int    `json:"version"`
	Ciphertext []byte `json:"ciphertext"`
}

// Open initializes a per-installation random 256-bit key exactly once. An
// OS-held lock serializes initialization across processes and releases on exit.
// Missing or corrupt keys are never replaced if encrypted profiles exist.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, ErrInvalidStore
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, ErrInvalidStore
	}
	if err := os.MkdirAll(absolute, 0700); err != nil {
		return nil, ErrInvalidStore
	}
	root, err := openDirectory(absolute)
	if err != nil {
		return nil, ErrInvalidStore
	}
	defer root.Close()
	lock, err := openRegular(root, ".init.lock", os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, ErrInvalidStore
	}
	defer lock.Close()
	if err := lockPrivateFile(lock); err != nil {
		return nil, ErrInvalidStore
	}
	defer unlockPrivateFile(lock)
	if err := root.Mkdir(profileDir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, ErrInvalidStore
	}
	profiles, err := openChildDirectory(root, profileDir)
	if err != nil {
		return nil, ErrInvalidStore
	}
	defer profiles.Close()
	encoded, err := readBounded(root, keyFile, maxKeyBytes)
	if errors.Is(err, os.ErrNotExist) {
		// A lost key cannot recover existing ciphertext. Do not silently make
		// the loss permanent by saving a new key over the expected location.
		directory, e := profiles.Open(".")
		if e != nil {
			return nil, ErrInvalidStore
		}
		entries, e := directory.Readdirnames(-1)
		directory.Close()
		if e != nil || len(entries) != 0 {
			return nil, ErrInvalidStore
		}
		key := make([]byte, 32)
		if _, e := rand.Read(key); e != nil {
			return nil, ErrInvalidStore
		}
		protected, e := protectKey(key)
		clear(key)
		if e != nil {
			return nil, ErrInvalidStore
		}
		encoded, e = json.Marshal(keyEnvelope{Version: formatVersion, Protection: keyProtection, Key: protected})
		if e != nil || writeAtomic(root, keyFile, encoded) != nil {
			return nil, ErrInvalidStore
		}
	} else if err != nil {
		return nil, ErrInvalidStore
	}
	var envelope keyEnvelope
	if decodeJSON(encoded, &envelope) != nil || envelope.Version != formatVersion || envelope.Protection != keyProtection {
		return nil, ErrInvalidStore
	}
	key, err := unprotectKey(envelope.Key)
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, ErrInvalidStore
	}
	return &Store{dir: absolute, key: key}, nil
}

// Path returns the ciphertext location, with no workspace name or path leaked
// into a filename. The full original workspace key also authenticates the data.
func (s *Store) Path(workspaceKey string) string {
	return filepath.Join(s.dir, profileDir, profileName(workspaceKey))
}

func (s *Store) Save(workspaceKey string, settings LLMSettings) error {
	if !validWorkspace(workspaceKey) {
		return ErrInvalidWorkspace
	}
	return s.saveValue(workspaceKey, settings)
}

func (s *Store) saveValue(workspaceKey string, value any) error {
	if !validWorkspace(workspaceKey) {
		return ErrInvalidWorkspace
	}
	plaintext, err := json.Marshal(value)
	if err != nil || len(plaintext) > maxProfileBytes/2 {
		return ErrInvalidSettings
	}
	defer clear(plaintext)
	aead, err := s.aead()
	if err != nil {
		return ErrInvalidStore
	}
	// NewGCMWithRandomNonce generates and prepends a fresh 96-bit nonce.
	ciphertext := aead.Seal(nil, nil, plaintext, additionalData(workspaceKey))
	encoded, err := json.Marshal(profileEnvelope{Version: formatVersion, Ciphertext: ciphertext})
	if err != nil {
		return ErrWrite
	}
	root, err := s.openProfiles()
	if err != nil {
		return ErrInvalidStore
	}
	defer root.Close()
	if err := writeAtomic(root, profileName(workspaceKey), encoded); err != nil {
		return ErrWrite
	}
	return nil
}

// Load returns os.ErrNotExist only when this workspace has no saved profile.
// Authentication failures and malformed data never return partial settings.
func (s *Store) Load(workspaceKey string) (LLMSettings, error) {
	var settings LLMSettings
	err := s.loadValue(workspaceKey, &settings)
	return settings, err
}

func (s *Store) loadValue(workspaceKey string, settings any) error {
	if !validWorkspace(workspaceKey) {
		return ErrInvalidWorkspace
	}
	root, err := s.openProfiles()
	if err != nil {
		return ErrInvalidStore
	}
	defer root.Close()
	encoded, err := readBounded(root, profileName(workspaceKey), maxProfileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	if err != nil {
		return err
	}
	return s.decodeValue(workspaceKey, encoded, settings)
}

func (s *Store) decodeValue(workspaceKey string, encoded []byte, settings any) error {
	var envelope profileEnvelope
	if decodeJSON(encoded, &envelope) != nil || envelope.Version != formatVersion {
		return ErrInvalidSettings
	}
	aead, err := s.aead()
	if err != nil {
		return ErrInvalidStore
	}
	plaintext, err := aead.Open(nil, nil, envelope.Ciphertext, additionalData(workspaceKey))
	if err != nil {
		return ErrInvalidSettings
	}
	defer clear(plaintext)
	if decodeJSON(plaintext, settings) != nil {
		return ErrInvalidSettings
	}
	return nil
}

func (s *Store) aead() (cipher.AEAD, error) {
	if len(s.key) != 32 {
		return nil, ErrInvalidStore
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (s *Store) openProfiles() (*os.Root, error) {
	root, err := openDirectory(s.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return openChildDirectory(root, profileDir)
}

func profileName(workspaceKey string) string {
	digest := sha256.Sum256([]byte(workspaceKey))
	return hex.EncodeToString(digest[:]) + ".json"
}

func validWorkspace(key string) bool {
	return strings.TrimSpace(key) != "" && len(key) <= 16<<10 && !strings.ContainsRune(key, '\x00')
}

func additionalData(key string) []byte { return []byte("OneByOne/private-llm/v1\x00" + key) }

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrInvalidSettings
	}
	return nil
}

func openDirectory(path string) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidStore
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, ErrInvalidStore
	}
	if err := restrictDirectory(root); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func openChildDirectory(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidStore
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, ErrInvalidStore
	}
	if err := restrictDirectory(root); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func restrictDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(0700)
}

// Check the opened inode before reading, locking, or modifying it. Root keeps
// traversal confined even if a path is swapped during these checks.
func openRegular(root *os.Root, name string, flags int) (*os.File, error) {
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return nil, ErrInvalidStore
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Create a stable file exclusively first, then open the existing inode.
	// Concurrent O_CREAT opens on macOS can transiently report ENOENT even
	// though another opener has just created the same lock file.
	var f *os.File
	var err error
	if flags&os.O_CREATE != 0 && flags&os.O_EXCL == 0 {
		f, err = root.OpenFile(name, flags|os.O_EXCL|noFollowFlag, 0600)
		if errors.Is(err, os.ErrExist) {
			f, err = root.OpenFile(name, flags&^os.O_CREATE|noFollowFlag, 0600)
		}
	} else {
		f, err = root.OpenFile(name, flags|noFollowFlag, 0600)
	}
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	current, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil || !opened.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		f.Close()
		return nil, ErrInvalidStore
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func readBounded(root *os.Root, name string, limit int64) ([]byte, error) {
	data, _, err := readBoundedSnapshot(root, name, limit)
	return data, err
}

// Keep the identity of the file that was read so a caller can remove only that
// invalid snapshot while holding its document lock. I/O failures are distinct
// from invalid contents and must never authorize discarding a saved document.
func readBoundedSnapshot(root *os.Root, name string, limit int64) ([]byte, os.FileInfo, error) {
	f, err := openRegular(root, name, os.O_RDONLY)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.Size() > limit {
		return nil, info, ErrInvalidSettings
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, info, err
	}
	if int64(len(data)) > limit {
		return nil, info, ErrInvalidSettings
	}
	return data, info, nil
}

// The caller holds the stable document lock across reading and removal. Root
// confines traversal; this last identity check also rejects a replaced path.
func removeSnapshot(root *os.Root, name string, snapshot os.FileInfo) error {
	current, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if snapshot == nil || !current.Mode().IsRegular() || !os.SameFile(snapshot, current) {
		return ErrInvalidStore
	}
	return root.Remove(name)
}

func writeAtomic(root *os.Root, name string, data []byte) error {
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return ErrInvalidStore
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := ".write-" + hex.EncodeToString(random)
	f, err := openRegular(root, temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, err = f.Write(append(data, '\n'))
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
	if err := root.Rename(temporary, name); err != nil {
		return err
	}
	if directory, err := root.Open("."); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
