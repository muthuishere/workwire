//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// detach puts the listener in its own session so it survives the hook process
// and the terminal that started it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
