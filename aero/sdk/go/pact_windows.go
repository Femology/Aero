//go:build windows

package aero

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsPact holds a Job Object handle for the process lifetime.
// When the Go supervisor exits or crashes, the handle is closed and the
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE flag terminates the shell instantly.
type windowsPact struct {
	job windows.Handle
}

func newPlatformPact() platformPact {
	return &windowsPact{}
}

func (p *windowsPact) apply(_ *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("CreateJobObject: %w", err)
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("SetInformationJobObject: %w", err)
	}

	p.job = job
	return nil
}

func (p *windowsPact) assign(cmd *exec.Cmd) error {
	if p.job == 0 || cmd.Process == nil {
		return fmt.Errorf("death pact: job object or process unavailable")
	}

	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(proc)

	if err := windows.AssignProcessToJobObject(p.job, proc); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}

func (p *windowsPact) close() {
	if p.job != 0 {
		_ = windows.CloseHandle(p.job)
		p.job = 0
	}
}
