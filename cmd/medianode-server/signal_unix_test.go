//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

func exeSuffix() string { return "" }

func prepareStop(*exec.Cmd) {}

func sendStop(cmd *exec.Cmd) error {
	return cmd.Process.Signal(syscall.SIGTERM)
}
