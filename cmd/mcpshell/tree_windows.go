//go:build windows

package main

import (
	"os/exec"
	"sync"

	"golang.org/x/sys/windows"
)

// processTree lets a timeout kill everything a command started, not only the command:
// killing go run or make leaves the program it built, or a recipe's commands, running.
// On Windows the command is put in a job object, which the processes it starts join.
type processTree struct {
	cmd *exec.Cmd

	mu  sync.Mutex
	job windows.Handle
}

// newProcessTree prepares cmd before it starts; its Cancel then kills the job.
func newProcessTree(cmd *exec.Cmd) *processTree {
	t := &processTree{cmd: cmd}
	cmd.Cancel = t.kill
	return t
}

// started puts the running command in a new job. A process it started before this
// is not in the job; the window is the few instructions since the command started.
// If the job cannot be set up, a timeout kills the command alone, as before.
//
// The pid cannot have been reused: os.Process holds a handle to the command until
// Wait, and Windows does not reuse the id of a process with an open handle.
func (t *processTree) started() {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(t.cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	t.mu.Lock()
	t.job = job
	t.mu.Unlock()
}

// close releases the job after Wait. Closing it does not end the processes in it.
func (t *processTree) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

func (t *processTree) kill() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 && windows.TerminateJobObject(t.job, 1) == nil {
		return nil
	}
	return t.cmd.Process.Kill()
}
