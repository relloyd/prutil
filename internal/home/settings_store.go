package home

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// applySave is a helper that performs the safe read-modify-parse-verify-write cycle.
func (s *Store) applySave(manualMsg string, edit func(data []byte) ([]byte, error), mutator func(c *Config)) error {
	if _, err := s.LoadOrCreateConfig(); err != nil {
		return err
	}

	target, err := filepath.EvalSymlinks(s.Path(ConfigFile))
	if err != nil {
		return fmt.Errorf("could not find %s: %w", s.Path(ConfigFile), err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", target, err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", target, err)
	}
	before, err := ParseConfig(data)
	if err != nil {
		return err
	}

	manual := fmt.Errorf("%s in %s by hand", manualMsg, target)
	edited, err := edit(data)
	if err != nil {
		return fmt.Errorf("%w; %w", err, manual)
	}

	want := before
	if mutator != nil {
		mutator(&want)
	}
	after, err := ParseConfig(edited)
	if err != nil || !reflect.DeepEqual(after, want) {
		return fmt.Errorf("could not change %s without disturbing the rest of it; %w", target, manual)
	}

	if string(edited) == string(data) {
		return nil
	}
	return replaceFile(target, edited, info.Mode().Perm())
}

// SaveSetting writes a scalar value into config.yaml at path.
func (s *Store) SaveSetting(path []string, value string, mutator func(c *Config)) error {
	msg := fmt.Sprintf("set %s to %s", dotted(path), value)
	return s.applySave(msg, func(data []byte) ([]byte, error) {
		return setScalar(data, path, value)
	}, mutator)
}

// SaveBlockScalar writes a multiline block scalar into config.yaml at path.
func (s *Store) SaveBlockScalar(path []string, text string, mutator func(c *Config)) error {
	msg := fmt.Sprintf("set %s", dotted(path))
	return s.applySave(msg, func(data []byte) ([]byte, error) {
		return setBlockScalar(data, path, text)
	}, mutator)
}

// SaveMapEntry sets a key-value entry in a mapping at path in config.yaml.
func (s *Store) SaveMapEntry(path []string, key, value string, mutator func(c *Config)) error {
	msg := fmt.Sprintf("set %s.%s", dotted(path), key)
	return s.applySave(msg, func(data []byte) ([]byte, error) {
		return setMapEntry(data, path, key, value)
	}, mutator)
}

// DeleteMapEntry removes an entry from a mapping at path in config.yaml.
func (s *Store) DeleteMapEntry(path []string, key string, mutator func(c *Config)) error {
	msg := fmt.Sprintf("delete %s.%s", dotted(path), key)
	return s.applySave(msg, func(data []byte) ([]byte, error) {
		return deleteMapEntry(data, path, key)
	}, mutator)
}

// SaveSequence sets a list of items at path in config.yaml.
func (s *Store) SaveSequence(path []string, items []string, mutator func(c *Config)) error {
	msg := fmt.Sprintf("set %s", dotted(path))
	return s.applySave(msg, func(data []byte) ([]byte, error) {
		return setSequence(data, path, items)
	}, mutator)
}

// ResetSetting deletes the setting at path from config.yaml, reverting to default.
func (s *Store) ResetSetting(path []string, mutator func(c *Config)) error {
	msg := fmt.Sprintf("reset %s", dotted(path))
	return s.applySave(msg, func(data []byte) ([]byte, error) {
		return deleteKey(data, path)
	}, mutator)
}
