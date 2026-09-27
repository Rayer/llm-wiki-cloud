//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package main

import (
	"errors"
	"os"
)

func lockCredentialFile(_ *os.File) (func() error, error) {
	return nil, errors.New("local credential file locking is unsupported on this platform")
}
