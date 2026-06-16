//go:build !windows

package aero

import (
	"os/exec"
	"syscall"
)

type unixPact struct{}

func newPlatformPact() platformPact { return &unixPact{} }

func (p *unixPact) apply(cmd *exec.Cmd) error {
	attr := &syscall.SysProcAttr{Setpgid: true}
	applyLinuxDeathSig(attr)
	cmd.SysProcAttr = attr
	return nil
}

func (p *unixPact) assign(_ *exec.Cmd) error { return nil }

func (p *unixPact) close() {}
