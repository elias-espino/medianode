//go:build windows

package transcoding

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Un Job Object con KILL_ON_JOB_CLOSE: su handle vive lo que el servidor, y
// cuando el proceso muere por cualquier motivo el sistema lo cierra y mata a
// todos los FFmpeg asignados (§12.5).
var (
	jobOnce   sync.Once
	jobHandle windows.Handle
	jobErr    error
)

func killOnCloseJob() (windows.Handle, error) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			jobErr = fmt.Errorf("crear Job Object: %w", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		_, err = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
		if err != nil {
			_ = windows.CloseHandle(h)
			jobErr = fmt.Errorf("configurar Job Object: %w", err)
			return
		}
		jobHandle = h
	})
	return jobHandle, jobErr
}

// prepare baja la prioridad: la transcodificación no debe quitar CPU al resto.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.BELOW_NORMAL_PRIORITY_CLASS}
}

// attach asigna el proceso al Job Object.
func attach(p *os.Process) error {
	job, err := killOnCloseJob()
	if err != nil {
		return err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return fmt.Errorf("abrir proceso: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		return fmt.Errorf("asignar al Job Object: %w", err)
	}
	return nil
}
