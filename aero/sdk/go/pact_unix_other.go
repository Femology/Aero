//go:build !linux && !windows

package aero

import "syscall"

func applyLinuxDeathSig(_ *syscall.SysProcAttr) {}
