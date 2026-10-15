//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureCompanionCPUProcess(cmd *exec.Cmd) {
	// A CPU wrapper launches its own model processes. Give each request a new
	// group so cancellation also stops descendants holding the output pipes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
