package filelock

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentWritersSerialize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard")
	var wg sync.WaitGroup
	var count, active atomic.Int64
	for range 16 {
		wg.Go(func() {
			unlock, err := Acquire(path)
			if err != nil {
				t.Error(err)
				return
			}
			defer unlock()
			if active.Add(1) != 1 {
				t.Error("file lock admitted concurrent writers")
			}
			runtime.Gosched()
			count.Add(1)
			active.Add(-1)
		})
	}
	wg.Wait()
	if count.Load() != 16 {
		t.Fatalf("count = %d", count.Load())
	}
}

func TestAcquireRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "lock")
	if err := os.Symlink(target, path); err != nil {
		t.Skip(err)
	}
	if unlock, err := Acquire(path); err == nil {
		unlock()
		t.Fatal("symlink lock accepted")
	}
}
