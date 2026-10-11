package main

import (
	"fmt"
	"io"
)

var version = "dev"
var commit = "unknown"

func handleVersionCommand(args []string, stdout io.Writer) bool {
	if len(args) != 1 || args[0] != "--version" {
		return false
	}
	fmt.Fprintf(stdout, "lwc-sync %s\n", version)
	return true
}
