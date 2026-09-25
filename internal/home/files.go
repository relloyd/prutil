package home

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile replaces path with data in one step, readable by its owner alone,
// creating the directory it lives in when that is missing.
func WriteFile(path string, data []byte) error {
	return replaceFile(path, data, filePerm)
}

// CreateOnce writes data to path unless something is already there, and
// returns whatever path holds afterwards.
//
// It is for a file prutil writes a default into and the reader then owns:
// once it exists, prutil never writes it again. The file is written in full
// under a temporary name and linked into place, so a reader never sees half of
// it, and a link refuses to replace a file another process created first,
// which is then the one returned.
func CreateOnce(path string, data []byte) ([]byte, error) {
	existing, err := os.ReadFile(path)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("could not read %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("could not create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return nil, fmt.Errorf("could not create %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := tmp.Chmod(filePerm); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("could not create %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("could not write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("could not write %s: %w", path, err)
	}
	if err := os.Link(tmp.Name(), path); errors.Is(err, fs.ErrExist) {
		return os.ReadFile(path)
	} else if err != nil {
		return nil, fmt.Errorf("could not create %s: %w", path, err)
	}
	return data, nil
}
