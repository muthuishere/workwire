//go:build windows

package main

import "os/exec"

// detach is a no-op on Windows: a child process already outlives its parent.
func detach(cmd *exec.Cmd) {}
