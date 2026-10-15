package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestInheritedCacheLockRejectsArbitraryPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", victim)

	unlock, acquired, _, err := acquireCacheRefreshLock()
	if err == nil || acquired || unlock != nil {
		t.Fatalf("arbitrary inherited lock accepted: acquired=%v unlock=%v err=%v", acquired, unlock != nil, err)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("victim changed: %v", statErr)
	}
}

func TestInheritedCacheLockAcceptsManagedPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", "")
	managed, err := expectedCacheRefreshLockDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeCacheRefreshLockOwnerToken(managed, "fixture-parent-token"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", managed)
	t.Setenv("WECHAT_CLI_CACHE_LOCK_TOKEN", "fixture-parent-token")

	unlock, acquired, got, err := acquireCacheRefreshLock()
	if err != nil || !acquired || unlock == nil || got != managed {
		t.Fatalf("managed lock rejected: acquired=%v got=%q err=%v", acquired, got, err)
	}
	unlock()
	if _, statErr := os.Stat(managed); !os.IsNotExist(statErr) {
		t.Fatalf("managed lock still exists: %v", statErr)
	}
}

func TestCacheLockHandoffCannotBeClaimedTwiceOrReleasedByParent(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", "")
	parentUnlock, acquired, path, err := acquireCacheRefreshLock()
	if err != nil || !acquired {
		t.Fatalf("parent acquisition: %v, %v", acquired, err)
	}
	parent, err := readCacheRefreshLockOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", path)
	t.Setenv("WECHAT_CLI_CACHE_LOCK_TOKEN", parent.Token)
	childUnlock, acquired, _, err := acquireCacheRefreshLock()
	if err != nil || !acquired {
		t.Fatalf("child acquisition: %v, %v", acquired, err)
	}
	defer childUnlock()
	if _, acquired, _, err := acquireCacheRefreshLock(); err == nil || acquired {
		t.Fatal("same handoff was claimed twice")
	}
	parentUnlock()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("parent removed child's lock: %v", err)
	}
}

func TestLegacyCacheLockFlagCannotBypassActiveWriter(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", "")
	unlock, acquired, _, err := acquireCacheRefreshLock()
	if err != nil || !acquired {
		t.Fatalf("first acquisition: %v, %v", acquired, err)
	}
	defer unlock()
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", "1")
	if _, acquired, _, err := acquireCacheRefreshLock(); err != nil || acquired {
		t.Fatalf("legacy flag bypassed lock: %v, %v", acquired, err)
	}
}

func TestCacheLockConcurrentAcquisitionHasSingleOwner(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", "")
	var wg sync.WaitGroup
	owners := make(chan func(), 16)
	errors := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			unlock, acquired, _, err := acquireCacheRefreshLock()
			if err != nil {
				errors <- err
			}
			if acquired {
				owners <- unlock
			}
		})
	}
	wg.Wait()
	close(owners)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if len(owners) != 1 {
		t.Errorf("concurrent owners = %d, want 1", len(owners))
	}
	for unlock := range owners {
		unlock()
	}
}

func TestCacheRefreshHandoffChildProcess(t *testing.T) {
	if os.Getenv("WECHAT_CLI_TEST_HANDOFF_CHILD") != "1" {
		return
	}
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
}

func TestCacheBackgroundHandoffOwnsLeaseBeforeClaim(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", "")
	parentUnlock, acquired, path, err := acquireCacheRefreshLock()
	if err != nil || !acquired {
		t.Fatalf("parent acquisition: %v, %v", acquired, err)
	}
	defer parentUnlock()
	parent, err := readCacheRefreshLockOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCacheRefreshHandoffChildProcess$")
	cmd.Env = append(os.Environ(), "WECHAT_CLI_TEST_HANDOFF_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := startCacheRefreshHandoff(cmd, path); err != nil {
		_ = stdin.Close()
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
	owner, err := readCacheRefreshLockOwner(path)
	if err != nil || owner.PID != cmd.Process.Pid || owner.Token == "" || owner.Token == parent.Token {
		t.Fatalf("handoff still belongs to parent: %#v, %v", owner, err)
	}
	parentUnlock()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("parent released the unclaimed child lease")
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	if cacheRefreshLockStale(path) {
		t.Fatal("live unclaimed child lease can be reaped")
	}
	if _, acquired, _, err := acquireCacheRefreshLock(); err != nil || acquired {
		t.Fatalf("third caller acquired the live child lease: %v, %v", acquired, err)
	}
}
