//go:build linux

package aero

import "syscall"

// applyLinuxDeathSig delivers SIGKILL to the shell if the Go supervisor dies unexpectedly.
func applyLinuxDeathSig(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGKILL
}
