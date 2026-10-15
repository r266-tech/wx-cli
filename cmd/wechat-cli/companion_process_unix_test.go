//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCompanionCPUCancellationStopsChildProcesses(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cpu")
	pidFile := filepath.Join(dir, "child.pid")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30 &\necho $! > \"$WECHAT_CLI_TEST_PID_FILE\"\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_CLI_COMPANION_CPU_BIN", script)
	t.Setenv("WECHAT_CLI_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("WECHAT_CLI_COMPANION_CPU_CWD", dir)
	t.Setenv("WECHAT_CLI_TEST_PID_FILE", pidFile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := companionRunCPU(ctx, companionPrompt{}, nil, companionAskRequest{}, nil)
		done <- err
	}()
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("fixture child did not start")
	}
	defer func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled CPU succeeded")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cancelled CPU retained output handles")
	}
	deadline = time.Now().Add(time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatal("cancelled CPU left child process alive")
	}
}

func TestCompanionCPUFailureTailRedactsBeforeTruncating(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cpu")
	payload := "#!/bin/sh\nprintf '%s\\n' 'error token=\"" + strings.Repeat("secret words ", 100) + "\"'\nexit 1\n"
	if err := os.WriteFile(script, []byte(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_CLI_COMPANION_CPU_BIN", script)
	t.Setenv("WECHAT_CLI_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("WECHAT_CLI_COMPANION_CPU_CWD", dir)
	_, err := companionRunCPU(context.Background(), companionPrompt{}, nil, companionAskRequest{}, nil)
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("unsafe CPU failure: %v", err)
	}
}

func TestCompanionCPUWrapperEarlyExitStopsDescendants(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cpu")
	pidFile := filepath.Join(dir, "child.pid")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30 &\necho $! > \"$WECHAT_CLI_TEST_PID_FILE\"\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_CLI_COMPANION_CPU_BIN", script)
	t.Setenv("WECHAT_CLI_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("WECHAT_CLI_COMPANION_CPU_CWD", dir)
	t.Setenv("WECHAT_CLI_TEST_PID_FILE", pidFile)
	_, err := companionRunCPU(context.Background(), companionPrompt{}, nil, companionAskRequest{}, nil)
	raw, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid <= 0 {
		t.Fatal("fixture child missing")
	}
	defer func() {
		p, _ := os.FindProcess(pid)
		if p != nil {
			_ = p.Kill()
		}
	}()
	if err == nil {
		t.Fatal("early exit with inherited pipes must report an error")
	}
	deadline := time.Now().Add(time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("CPU returned %v but left descendant alive", err)
	}
}
