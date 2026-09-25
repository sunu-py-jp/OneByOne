package privateconfig

import (
	"errors"
	"os"
	"path/filepath"
)

// Connection and Registry are encrypted as one document. Neither friendly names
// nor credentials are written to a plaintext index. Workspace references live
// in each workspace setting.json.
type Connection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	LLMSettings
}

type Registry struct {
	Version     int          `json:"version"`
	Connections []Connection `json:"connections"`
}

const registryVersion = 2

// This storage identity stays fixed when the document schema changes. An
// unsupported document is discarded rather than imported or left unreadable.
const registryKey = "OneByOne/global-connections/v1"

func (s *Store) ConnectionsDirectory() string { return filepath.Join(s.dir, profileDir) }

func (s *Store) LoadConnections() (Registry, error) {
	unlock, err := s.lockConnections()
	if err != nil {
		return Registry{}, err
	}
	defer unlock()
	return s.loadConnectionsLocked()
}

// Always read inside the connection lock, including on the recovery path, so
// another app's successful repair cannot be deleted using an earlier read.
func (s *Store) loadConnectionsLocked() (Registry, error) {
	empty := Registry{Version: registryVersion, Connections: []Connection{}}
	root, err := s.openProfiles()
	if err != nil {
		return Registry{}, err
	}
	defer root.Close()
	name := profileName(registryKey)
	encoded, snapshot, err := readBoundedSnapshot(root, name, maxProfileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	var r Registry
	if err == nil {
		err = s.decodeValue(registryKey, encoded, &r)
	}
	if err == nil {
		err = validateRegistry(r)
	}
	if errors.Is(err, ErrInvalidSettings) {
		if err := removeSnapshot(root, name, snapshot); err != nil {
			return Registry{}, err
		}
		return empty, nil
	}
	if err != nil {
		return Registry{}, err
	}
	if r.Connections == nil {
		r.Connections = []Connection{}
	}
	return r, nil
}

func validateRegistry(r Registry) error {
	if r.Version != registryVersion {
		return ErrInvalidSettings
	}
	seen := map[string]bool{}
	for _, c := range r.Connections {
		if c.ID == "" || seen[c.ID] {
			return ErrInvalidSettings
		}
		seen[c.ID] = true
	}
	return nil
}

// UpdateConnections holds a stable OS lock across read/modify/atomic replacement,
// so independent app processes cannot lose each other's connection definitions.
// The callback must not call LoadConnections or UpdateConnections recursively.
func (s *Store) UpdateConnections(update func(*Registry) error) (Registry, error) {
	unlock, err := s.lockConnections()
	if err != nil {
		return Registry{}, err
	}
	defer unlock()
	r, err := s.loadConnectionsLocked()
	if err != nil {
		return Registry{}, err
	}
	if err = update(&r); err != nil {
		return Registry{}, err
	}
	if err = validateRegistry(r); err != nil {
		return Registry{}, err
	}
	if err = s.saveValue(registryKey, r); err != nil {
		return Registry{}, err
	}
	return r, nil
}

func (s *Store) lockConnections() (func(), error) {
	root, err := openDirectory(s.dir)
	if err != nil {
		return nil, err
	}
	lock, err := openRegular(root, ".connections.lock", os.O_RDWR|os.O_CREATE)
	if err != nil {
		root.Close()
		return nil, err
	}
	if err = lockPrivateFile(lock); err != nil {
		lock.Close()
		root.Close()
		return nil, err
	}
	return func() {
		unlockPrivateFile(lock)
		lock.Close()
		root.Close()
	}, nil
}
