//go:build windows

package ffmpeg

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Job Object с флагом KILL_ON_JOB_CLOSE: когда наш процесс завершается (в том
// числе аварийно), Windows закрывает дескриптор задания и убивает все
// привязанные к нему процессы ffmpeg.exe.
var (
	jobOnce   sync.Once
	jobHandle windows.Handle
	jobErr    error
)

func processJob() (windows.Handle, error) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			jobErr = fmt.Errorf("CreateJobObject: %w", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		_, err = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
		if err != nil {
			_ = windows.CloseHandle(h)
			jobErr = fmt.Errorf("SetInformationJobObject: %w", err)
			return
		}
		jobHandle = h
	})
	return jobHandle, jobErr
}

// assignToJob привязывает запущенный процесс к заданию процесса.
func assignToJob(p *os.Process) error {
	job, err := processJob()
	if err != nil {
		return err
	}
	const access = windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE
	h, err := windows.OpenProcess(access, false, uint32(p.Pid))
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}
