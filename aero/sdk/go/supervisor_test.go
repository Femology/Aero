package aero

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestDeathPactApplyUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix death pact")
	}
	pact := newDeathPact()
	cmd := exec.Command("true")
	if err := pact.apply(cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("expected Setpgid on SysProcAttr")
	}
}

func TestDeathPactApplyWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows death pact")
	}
	pact := newDeathPact()
	cmd := exec.Command("cmd", "/c", "exit")
	if err := pact.apply(cmd); err != nil {
		t.Fatal(err)
	}
}
