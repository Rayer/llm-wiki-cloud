//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"io"
	"os"
	"syscall"
)

func writeRawSyncObservation(line string) {
	fd, err := syscall.Dup(int(os.Stderr.Fd()))
	if err != nil {
		return
	}
	if fd < 3 {
		_ = syscall.Close(fd)
		return
	}
	syscall.CloseOnExec(fd)
	writer := os.NewFile(uintptr(fd), "lwc-sync observation")
	if writer == nil {
		_ = syscall.Close(fd)
		return
	}
	defer writer.Close()
	_, _ = io.WriteString(writer, line)
}
