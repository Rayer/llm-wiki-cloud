//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package main

func syncDirectory(string) error { return nil }
