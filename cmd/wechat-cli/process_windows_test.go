//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestProcessAliveDistinguishesExitedProcess(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("current process should be alive")
	}
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	// Retain a kernel handle after Wait so the exited process object remains
	// openable: this is where os.FindProcess alone gives the wrong result.
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		_ = cmd.Wait()
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if processAlive(pid) {
		t.Fatal("exited process was reported alive")
	}
}
