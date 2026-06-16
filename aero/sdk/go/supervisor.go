package aero

import (
	"os/exec"
	"sync"
)

// deathPact enforces co-termination between the Go supervisor and Rust shell.
// Windows: Job Object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE (handle kept alive).
// Unix: setpgid process group (+ Pdeathsig on Linux).
type deathPact struct {
	mu   sync.Mutex
	impl platformPact
}

type platformPact interface {
	apply(cmd *exec.Cmd) error
	assign(cmd *exec.Cmd) error
	close()
}

func newDeathPact() *deathPact {
	return &deathPact{impl: newPlatformPact()}
}

func (p *deathPact) apply(cmd *exec.Cmd) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.impl.apply(cmd)
}

func (p *deathPact) assign(cmd *exec.Cmd) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.impl.assign(cmd)
}

func (p *deathPact) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.impl.close()
}
