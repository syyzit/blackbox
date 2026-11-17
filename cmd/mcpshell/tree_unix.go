//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// processTree lets a timeout kill everything a command started, not only the command:
// killing go run or make leaves the program it built, or a recipe's commands, running.
// On Unix the command leads a process group of its own, which its children inherit.
type processTree struct {
	cmd *exec.Cmd
}

// newProcessTree prepares cmd before it starts; its Cancel then kills the group.
func newProcessTree(cmd *exec.Cmd) *processTree {
	t := &processTree{cmd: cmd}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = t.kill
	return t
}

// started has nothing to do: the group was formed when the command started.
func (t *processTree) started() {}

// close has nothing to release.
func (t *processTree) close() {}

func (t *processTree) kill() error {
	// A negative pid names the group, whose id is the leader's pid.
	err := syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
