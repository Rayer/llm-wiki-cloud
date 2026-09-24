//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockCredentialFile(file *os.File) (func() error, error) {
	lock := &windows.Overlapped{}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, lock); err != nil {
		return nil, err
	}
	return func() error { return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, lock) }, nil
}
