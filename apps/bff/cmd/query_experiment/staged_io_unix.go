//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const stagedMaxBytes int64 = 512 << 20

type stagedFile struct {
	Data  []byte
	Mtime int64
}
type stagedTree map[string]stagedFile

func stagedHash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
func stagedExecutableDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > stagedMaxBytes {
		return "", errors.New("executable must be a bounded regular file")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, before.Size()+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || n != before.Size() || before.ModTime() != after.ModTime() {
		return "", errors.New("executable changed during read")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
func stagedJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func (t stagedTree) digest() string {
	m := map[string]string{}
	for p, f := range t {
		m[p] = stagedHash(f.Data)
	}
	return stagedHash(stagedJSON(m))
}
func (t stagedTree) mtimes() map[string]int64 {
	m := map[string]int64{}
	for p, f := range t {
		m[p] = f.Mtime
	}
	return m
}
func (t stagedTree) subset(prefix string) stagedTree {
	out := stagedTree{}
	for p, f := range t {
		if strings.HasPrefix(p, prefix) {
			out[strings.TrimPrefix(p, prefix)] = f
		}
	}
	return out
}
func (t stagedTree) corpus() string {
	out := stagedTree{}
	for p, f := range t {
		if strings.HasPrefix(p, "wiki/") || p == "cache/concepts.jsonl" {
			out[p] = f
		}
	}
	return out.digest()
}

// Directory descriptors and O_NOFOLLOW prevent a renamed ancestor or symlink
// swap from escaping the bounded snapshot read. Directory count/depth are bounded too.
func stagedReadTree(root string) (stagedTree, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return nil, errors.New("snapshot must be a canonical directory")
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	files := stagedTree{}
	var total int64
	entries := 0
	var walk func(int, string, int) error
	walk = func(fd int, prefix string, depth int) error {
		dir := os.NewFile(uintptr(fd), prefix)
		defer dir.Close()
		if depth > 128 {
			return errors.New("snapshot depth limit exceeded")
		}
		names, err := dir.Readdirnames(10001)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		sort.Strings(names)
		for _, name := range names {
			entries++
			if entries > 10000 {
				return errors.New("snapshot file/directory limit exceeded")
			}
			var before unix.Stat_t
			if err := unix.Fstatat(fd, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			kind := before.Mode & unix.S_IFMT
			if kind != unix.S_IFDIR && kind != unix.S_IFREG {
				return errors.New("snapshot contains a symlink or special file")
			}
			flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
			if kind == unix.S_IFDIR {
				flags |= unix.O_DIRECTORY
			}
			child, err := unix.Openat(fd, name, flags, 0)
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			err = unix.Fstat(child, &opened)
			if err != nil || opened.Ino != before.Ino || opened.Dev != before.Dev {
				unix.Close(child)
				return errors.New("snapshot changed during read")
			}
			rel := prefix + name
			if kind == unix.S_IFDIR {
				if err := walk(child, rel+"/", depth+1); err != nil {
					return err
				}
				continue
			}
			f := os.NewFile(uintptr(child), rel)
			info, err := f.Stat()
			if err != nil {
				f.Close()
				return err
			}
			total += info.Size()
			if info.Size() < 0 || total > stagedMaxBytes {
				f.Close()
				return errors.New("snapshot size limit exceeded")
			}
			b, err := io.ReadAll(io.LimitReader(f, info.Size()+1))
			after, statErr := f.Stat()
			f.Close()
			if err != nil || statErr != nil || int64(len(b)) != info.Size() || after.ModTime() != info.ModTime() {
				return errors.New("snapshot changed during read")
			}
			files[rel] = stagedFile{b, info.ModTime().UnixNano()}
		}
		return nil
	}
	if err = walk(fd, "", 0); err != nil {
		return nil, err
	}
	return files, nil
}

func stagedPutTree(root string, files stagedTree) error {
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	for p, f := range files {
		if _, err := snapshotPathComponents(p); err != nil {
			return err
		}
		dest := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = file.Write(f.Data)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		timestamp := time.Unix(0, f.Mtime)
		if err := os.Chtimes(dest, timestamp, timestamp); err != nil {
			return err
		}
	}
	return nil
}
func stagedAtomic(path string, v any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".receipt-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(stagedJSON(v)); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

// Only bounded JSON diagnostics are retained. Provider console output is discarded.
type stagedCapture struct{ bytes.Buffer }

func (b *stagedCapture) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 1<<20 {
		keep := min(n, (1<<20)-b.Len())
		b.Buffer.Write(p[:keep])
	}
	return n, nil
}

var stagedCategory = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,80}$`)

func stagedChild(ctx context.Context, argv, env []string, cwd string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.New("child canceled before start")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	var stdout stagedCapture
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, errors.New("child could not start")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		// Descendants must never outlive the stage, including after an early parent exit.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil {
			var d struct {
				ErrorType string `json:"error_type"`
			}
			_ = json.Unmarshal(stdout.Bytes(), &d)
			kind := ""
			if stagedCategory.MatchString(d.ErrorType) {
				kind = "; " + d.ErrorType
			}
			return nil, fmt.Errorf("child failed (exit %d%s); stage output not accepted", cmd.ProcessState.ExitCode(), kind)
		}
		return stdout.Bytes(), nil
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(300 * time.Millisecond)
		defer timer.Stop()
		waited := false
		select {
		case <-done:
			waited = true
		case <-timer.C:
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if !waited {
			<-done
		}
		return nil, errors.New("child interrupted or exceeded --timeout; partial workspace retained")
	}
}
