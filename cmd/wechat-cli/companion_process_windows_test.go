//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCompanionExecutableFileWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cpu.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !companionExecutableFile(path) {
		t.Fatal("Windows executable rejected for lacking Unix permission bits")
	}
	if companionExecutableFile(filepath.Dir(path)) {
		t.Fatal("directory accepted as executable")
	}
}

func TestCompanionCPUProcessCancellationWindows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", "ping -n 30 127.0.0.1 >NUL")
	configureCompanionCPUProcess(cmd)
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled CPU succeeded")
	}
}
