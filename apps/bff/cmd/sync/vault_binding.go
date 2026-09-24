package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const vaultBindingFileName = ".lwc-sync.json"

type vaultBindingConfig struct {
	Host      string `json:"host"`
	WikiID    string `json:"wiki_id"`
	ProjectID string `json:"project_id"`
	BindingID string `json:"binding_id"`
}

func vaultBindingPath(vault string) string {
	return filepath.Join(vault, vaultBindingFileName)
}

func prepareVaultBinding(vault, authHost, projectID string) (vaultBindingConfig, error) {
	authHost, err := normalizeAuthOrigin(authHost)
	if err != nil || !validVaultID(projectID) {
		return vaultBindingConfig{}, errors.New("provide a valid auth/control-plane host and Project ID")
	}
	vault, err = filepath.Abs(strings.TrimSpace(vault))
	if err != nil {
		return vaultBindingConfig{}, errors.New("invalid vault path")
	}
	info, err := os.Stat(vault)
	if err != nil || !info.IsDir() {
		return vaultBindingConfig{}, errors.New("vault path must be an existing directory")
	}
	var result vaultBindingConfig
	err = withVaultBindingLock(vault, func() error {
		existing, err := readVaultBinding(vault)
		if err == nil {
			if existing.Host != authHost {
				return errors.New("vault binding belongs to a different auth/control-plane host")
			}
			if existing.BindingID != "" {
				return errors.New("vault already has a binding; use `lwc-sync binding reauthorize` explicitly")
			}
			if existing.ProjectID != "" && existing.ProjectID != projectID {
				return errors.New("vault has an unfinished binding request for a different Project ID")
			}
			result = existing
			result.ProjectID = projectID
			return writeVaultBinding(vault, result)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		wikiID, err := newWikiID()
		if err != nil {
			return err
		}
		result = vaultBindingConfig{Host: authHost, WikiID: wikiID, ProjectID: projectID}
		return writeVaultBinding(vault, result)
	})
	if err != nil {
		return vaultBindingConfig{}, err
	}
	return result, nil
}

func loadVaultBinding(vault string) (vaultBindingConfig, error) {
	vault, err := filepath.Abs(strings.TrimSpace(vault))
	if err != nil {
		return vaultBindingConfig{}, errors.New("invalid vault path")
	}
	return readVaultBinding(vault)
}

func readVaultBinding(vault string) (vaultBindingConfig, error) {
	path := vaultBindingPath(vault)
	if err := rejectSymlink(path); err != nil {
		return vaultBindingConfig{}, err
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return vaultBindingConfig{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return vaultBindingConfig{}, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	var binding vaultBindingConfig
	if err := decoder.Decode(&binding); err != nil {
		return vaultBindingConfig{}, errors.New("invalid .lwc-sync.json binding file")
	}
	host, err := normalizeAuthOrigin(binding.Host)
	if err != nil || host != binding.Host || !validVaultID(binding.WikiID) || (binding.ProjectID != "" && !validVaultID(binding.ProjectID)) || (binding.BindingID != "" && !validVaultID(binding.BindingID)) {
		return vaultBindingConfig{}, errors.New("invalid .lwc-sync.json binding fields")
	}
	return binding, nil
}

func saveVaultBindingID(vault string, expected vaultBindingConfig, bindingID string) error {
	if !validVaultID(bindingID) {
		return errors.New("invalid sync binding ID")
	}
	vault, err := filepath.Abs(strings.TrimSpace(vault))
	if err != nil {
		return errors.New("invalid vault path")
	}
	return withVaultBindingLock(vault, func() error {
		current, err := readVaultBinding(vault)
		if err != nil {
			return err
		}
		if current.Host != expected.Host || current.WikiID != expected.WikiID || current.ProjectID != expected.ProjectID {
			return errors.New("vault binding changed while the server request was in progress")
		}
		current.BindingID = bindingID
		return writeVaultBinding(vault, current)
	})
}

func writeVaultBinding(vault string, binding vaultBindingConfig) error {
	data, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWritePrivateFile(vaultBindingPath(vault), data)
}

func withVaultBindingLock(vault string, fn func() error) error {
	lockPath := vaultBindingPath(vault) + ".lock"
	if err := rejectSymlink(lockPath); err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lock.Chmod(0o600); err != nil {
		return err
	}
	unlock, err := lockCredentialFile(lock)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func newWikiID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("create vault identity: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func validVaultID(value string) bool {
	return strings.TrimSpace(value) != "" && value != "." && value != ".." && len(value) <= 128 && !strings.ContainsAny(value, `/\`+"\x00")
}
