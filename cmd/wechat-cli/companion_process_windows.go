//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func configureCompanionCPUProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// /T scopes termination to this wrapper and the processes it launched.
		if err := exec.CommandContext(ctx, "taskkill.exe", "/PID", fmt.Sprint(cmd.Process.Pid), "/T", "/F").Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
