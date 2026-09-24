//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import "os"

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
