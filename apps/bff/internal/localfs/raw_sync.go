package localfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"cloud.google.com/go/storage"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

type lockedRawFile struct {
	*os.File
	unlock func()
}

func (f *lockedRawFile) Close() error {
	err := f.File.Close()
	f.unlock()
	return err
}

func (c *Client) ListSyncRawFiles(ctx context.Context) ([]store.RawSyncFile, error) {
	rawDir, err := c.fullPath("raw")
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(rawDir)
	if errors.Is(err, os.ErrNotExist) {
		return []store.RawSyncFile{}, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("raw path must be a real directory")
	}
	files := make([]store.RawSyncFile, 0)
	var total int64
	err = filepath.WalkDir(rawDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == rawDir {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("raw tree contains a symlink: %s", filepath.Base(path))
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("raw tree contains an unsupported file: %s", filepath.Base(path))
		}
		rel, err := filepath.Rel(rawDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := store.ValidateRawSyncPath(rel); err != nil {
			return err
		}
		if len(files) >= store.MaxRawSyncFiles {
			return errors.New("raw tree exceeds the file-count limit")
		}
		unlock, err := lockRawSyncPath(c, "raw/"+rel)
		if err != nil {
			return err
		}
		item, file, err := inspectRawSyncFile(ctx, path)
		if err != nil {
			unlock()
			return err
		}
		_ = file.Close()
		unlock()
		if item.Size > store.MaxRawSyncFileBytes || total > store.MaxRawSyncTotalBytes-item.Size {
			return errors.New("raw tree exceeds sync size limits")
		}
		item.Path = rel
		files = append(files, item)
		total += item.Size
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (c *Client) OpenSyncRawFile(ctx context.Context, rel, expectedGeneration string) (io.ReadCloser, int64, error) {
	if err := store.ValidateRawSyncPath(rel); err != nil {
		return nil, 0, err
	}
	path, err := c.fullPath(filepath.ToSlash(filepath.Join("raw", rel)))
	if err != nil {
		return nil, 0, err
	}
	unlock, err := lockRawSyncPath(c, "raw/"+rel)
	if err != nil {
		return nil, 0, err
	}
	item, file, err := inspectRawSyncFile(ctx, path)
	if err != nil {
		unlock()
		return nil, 0, err
	}
	if item.Generation != expectedGeneration {
		_ = file.Close()
		unlock()
		return nil, 0, store.ErrRawSyncConflict
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		unlock()
		return nil, 0, err
	}
	return &lockedRawFile{File: file, unlock: unlock}, item.Size, nil
}

func (c *Client) WriteSyncRawFile(ctx context.Context, rel string, body io.Reader, expectedGeneration, expectedSHA256 string) (string, error) {
	if err := store.ValidateRawSyncPath(rel); err != nil || !store.ValidRawSyncSHA256(expectedSHA256) {
		return "", errors.New("invalid raw sync upload")
	}
	root, err := c.projectRoot()
	if err != nil {
		return "", err
	}
	temporaryDir := filepath.Join(root, ".lwc-sync-staging")
	if err := os.MkdirAll(temporaryDir, 0o700); err != nil {
		return "", err
	}
	if info, err := os.Lstat(temporaryDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("raw sync staging path is invalid")
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", errors.New("could not create temporary raw sync file")
	}
	temporaryPath := filepath.Join(temporaryDir, hex.EncodeToString(token))
	temporary, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer os.Remove(temporaryPath)
	hasher := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(body, store.MaxRawSyncFileBytes+1))
	if copyErr == nil && count > store.MaxRawSyncFileBytes {
		copyErr = errors.New("raw sync file exceeds the per-file limit")
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if copyErr == nil && digest != expectedSHA256 {
		copyErr = errors.New("raw sync upload digest did not match the listed local file")
	}
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	target, err := c.fullPath(filepath.ToSlash(filepath.Join("raw", rel)))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if _, err := c.fullPath(filepath.ToSlash(filepath.Join("raw", rel))); err != nil {
		return "", err
	}
	unlock, err := lockRawSyncPath(c, "raw/"+rel)
	if err != nil {
		return "", err
	}
	defer unlock()
	currentGeneration := ""
	if _, err := os.Lstat(target); err == nil {
		current, currentFile, inspectErr := inspectRawSyncFile(ctx, target)
		if inspectErr != nil {
			return "", inspectErr
		}
		_ = currentFile.Close()
		currentGeneration = current.Generation
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if currentGeneration != expectedGeneration {
		return "", store.ErrRawSyncConflict
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return "", err
	}
	updated, file, err := inspectRawSyncFile(ctx, target)
	if err != nil {
		return "", fmt.Errorf("%w: %w", store.ErrRawSyncCommitUncertain, err)
	}
	_ = file.Close()
	return updated.Generation, nil
}

func inspectRawSyncFile(ctx context.Context, path string) (store.RawSyncFile, *os.File, error) {
	if err := ctx.Err(); err != nil {
		return store.RawSyncFile{}, nil, err
	}
	if info, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store.RawSyncFile{}, nil, storage.ErrObjectNotExist
		}
		return store.RawSyncFile{}, nil, err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return store.RawSyncFile{}, nil, errors.New("raw sync path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store.RawSyncFile{}, nil, storage.ErrObjectNotExist
		}
		return store.RawSyncFile{}, nil, err
	}
	before, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return store.RawSyncFile{}, nil, err
	}
	hasher := sha256.New()
	count, err := io.Copy(hasher, io.LimitReader(file, store.MaxRawSyncFileBytes+1))
	if err != nil || count > store.MaxRawSyncFileBytes {
		_ = file.Close()
		return store.RawSyncFile{}, nil, errors.New("raw sync file could not be read within limits")
	}
	after, err := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, pathInfo) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || count != after.Size() {
		_ = file.Close()
		return store.RawSyncFile{}, nil, store.ErrRawSyncConflict
	}
	return store.RawSyncFile{
		Size: count, SHA256: hex.EncodeToString(hasher.Sum(nil)),
		Generation: fmt.Sprintf("%d-%x", after.ModTime().UnixNano(), hasher.Sum(nil)),
	}, file, nil
}

func lockRawSyncPath(c *Client, rel string) (func(), error) {
	root, err := c.projectRoot()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(rel))
	lockDir := filepath.Join(root, ".lwc-sync-locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(lockDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("raw sync lock path is invalid")
	}
	lock, err := os.OpenFile(filepath.Join(lockDir, hex.EncodeToString(sum[:])), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}

func withRawSyncFileLock(c *Client, relPath string, fn func() error) error {
	return withRawSyncFileLocks(c, []string{relPath}, fn)
}

func withRawSyncFileLocks(c *Client, relPaths []string, fn func() error) error {
	paths := make([]string, 0, len(relPaths))
	seen := make(map[string]struct{}, len(relPaths))
	for _, relPath := range relPaths {
		clean := filepath.ToSlash(filepath.Clean(relPath))
		if !strings.HasPrefix(clean, "raw/") {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		paths = append(paths, clean)
	}
	sort.Strings(paths)
	var unlocks []func()
	for _, path := range paths {
		unlock, err := lockRawSyncPath(c, path)
		if err != nil {
			for i := len(unlocks) - 1; i >= 0; i-- {
				unlocks[i]()
			}
			return err
		}
		unlocks = append(unlocks, unlock)
	}
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()
	return fn()
}

var _ store.RawSyncStore = (*Client)(nil)
