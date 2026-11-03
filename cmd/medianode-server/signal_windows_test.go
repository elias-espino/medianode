//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

func exeSuffix() string { return ".exe" }

func prepareStop(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// sendStop usa CTRL_BREAK, que Go entrega como os.Interrupt. Requiere una
// consola compartida; sin ella la llamada falla y la prueba se omite.
func sendStop(cmd *exec.Cmd) error {
	const ctrlBreakEvent = 1
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")
	if ok, _, err := proc.Call(ctrlBreakEvent, uintptr(cmd.Process.Pid)); ok == 0 {
		return err
	}
	return nil
}
