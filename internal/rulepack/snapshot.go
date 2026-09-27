package rulepack

import "os"

// Snapshot opens an app-owned immutable version. It requires every rule id,
// while an otherwise invalid rule remains identifiable for repair or deletion.
// External documents use Read.
func Snapshot(path string) (*Package, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	docs, err := decodeDocument(data)
	if err != nil {
		return nil, err
	}
	return identify(docs)
}

// Extract writes into an unpublished, exclusively owned staging directory.
func Extract(p *Package, directory string) error {
	b, err := encodeDocument(p)
	if err != nil {
		return err
	}
	root, err := openDirectory(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile("rules.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(b)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
