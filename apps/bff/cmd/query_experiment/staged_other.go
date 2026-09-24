//go:build !darwin && !linux

package main

import (
	"fmt"
	"os"
)

func stagedContainerCommand([]string) bool { return true }

func stagedMain([]string) int {
	fmt.Fprintln(os.Stderr, "staged experiments require macOS or Linux for safe snapshots and process groups")
	return 1
}
