//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package main

import (
	"io"
	"os"
)

func writeRawSyncObservation(line string) {
	_, _ = io.WriteString(os.Stderr, line)
}
