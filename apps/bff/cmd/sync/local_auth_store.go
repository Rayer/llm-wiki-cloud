package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	localAuthDirectoryName = "lwc-sync"
	localConfigFileName    = "config.json"
	localCredentialsName   = "credentials.json"
	localLockFileName      = "credentials.lock"
)

var errCLILocalAuthNotFound = errors.New("local CLI credentials not found")

// cliLocalConfig contains non-secret client settings. AuthHost always names
// the Auth/control-plane origin; sync data services use their own future URL.
type cliLocalConfig struct {
	AuthHost string `json:"auth_host"`
}

// cliLocalCredentials contains secrets and is written only to mode 0600.
type cliLocalCredentials struct {
	AuthHost     string `json:"auth_host"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	SessionID    string `json:"session_id"`
	UserID       string `json:"user_id"`
	Role         string `json:"role"`
	AuthVersion  int64  `json:"auth_version"`
}

type localAuthStore struct {
	dir string
}

func defaultLocalAuthStore() (*localAuthStore, error) {
	root := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(home, ".config")
	}
	return newLocalAuthStoreAt(filepath.Join(root, localAuthDirectoryName)), nil
}

func newLocalAuthStoreAt(dir string) *localAuthStore {
	return &localAuthStore{dir: dir}
}

func (s *localAuthStore) configPath() string      { return filepath.Join(s.dir, localConfigFileName) }
func (s *localAuthStore) credentialsPath() string { return filepath.Join(s.dir, localCredentialsName) }
func (s *localAuthStore) lockPath() string        { return filepath.Join(s.dir, localLockFileName) }

func (s *localAuthStore) ensureDir() error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return errors.New("local auth directory is not configured")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(s.dir, 0o700)
}

func (s *localAuthStore) withLock(fn func() error) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	if err := rejectSymlink(s.lockPath()); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
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

func (s *localAuthStore) loadConfig() (cliLocalConfig, error) {
	var config cliLocalConfig
	if err := readPrivateJSON(s.configPath(), &config); err != nil {
		return config, err
	}
	if err := validateAuthOrigin(config.AuthHost); err != nil {
		return cliLocalConfig{}, fmt.Errorf("invalid auth_host in local config")
	}
	return config, nil
}

func (s *localAuthStore) saveConfig(config cliLocalConfig) error {
	if err := validateAuthOrigin(config.AuthHost); err != nil {
		return errors.New("auth/control-plane host must be an https origin (or local http origin)")
	}
	return writePrivateJSON(s.configPath(), config)
}

func (s *localAuthStore) loadCredentials() (cliLocalCredentials, error) {
	var credentials cliLocalCredentials
	if err := readPrivateJSON(s.credentialsPath(), &credentials); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cliLocalCredentials{}, errCLILocalAuthNotFound
		}
		return credentials, err
	}
	if validateAuthOrigin(credentials.AuthHost) != nil || credentials.AccessToken == "" || credentials.RefreshToken == "" {
		return cliLocalCredentials{}, errCLILocalAuthNotFound
	}
	return credentials, nil
}

func (s *localAuthStore) saveCredentials(credentials cliLocalCredentials) error {
	if validateAuthOrigin(credentials.AuthHost) != nil || credentials.AccessToken == "" || credentials.RefreshToken == "" {
		return errors.New("invalid local CLI credentials")
	}
	return writePrivateJSON(s.credentialsPath(), credentials)
}

func (s *localAuthStore) clearCredentials() error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	if err := rejectSymlink(s.credentialsPath()); err != nil {
		return err
	}
	if err := os.Remove(s.credentialsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(s.dir)
}

func readPrivateJSON(path string, value interface{}) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid local CLI auth file")
	}
	return nil
}

func writePrivateJSON(path string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := rejectSymlink(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWritePrivateFile(path, data)
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing symlink for local auth file")
	}
	if !info.Mode().IsRegular() {
		return errors.New("local auth path is not a regular file")
	}
	return nil
}

func atomicWritePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".lwc-sync-*tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}
