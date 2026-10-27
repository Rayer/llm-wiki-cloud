//go:build !byokprofilefixture

package main

func profileFixtureEnvironment() []string        { return nil }
func profileFixtureScript(adapter string) string { return adapter }
