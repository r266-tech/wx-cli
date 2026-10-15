package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/r266-tech/wx-cli/v2/internal/filelock"
)

func TestFailedExportPreservesExistingFile(t *testing.T) {
	for _, format := range []string{"invalid", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "existing.jsonl")
			if err := os.WriteFile(path, []byte("previous complete export"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
			_, err := (&server{}).writeLiveExportMessages(map[string]any{"chat": "missing fixture"}, path, format, "agent")
			if err == nil {
				t.Fatal("expected invalid format or unavailable source error")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "previous complete export" {
				t.Fatalf("failed export destroyed the previous output: %q, %v", got, err)
			}
		})
	}
}

func TestDigestFiltersDoNotAdvanceUnfilteredCheckpoint(t *testing.T) {
	s, talker := readMessageFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	filtered, err := s.toolDigestSource(map[string]any{"talker": talker, "since_last": true, "from_me": true, "include_media_paths": false})
	if err != nil {
		t.Fatal(err)
	}
	if got := filtered.(map[string]any)["message_count"]; got != 2 {
		t.Fatalf("filtered messages = %v", got)
	}
	unfiltered, err := s.toolDigestSource(map[string]any{"talker": talker, "since_last": true, "include_media_paths": false})
	if err != nil {
		t.Fatal(err)
	}
	if got := unfiltered.(map[string]any)["message_count"]; got != 4 {
		t.Fatalf("filtered checkpoint skipped unfiltered messages: %v", got)
	}
}

func TestDigestAccountsUseIndependentCheckpoints(t *testing.T) {
	first, talker := readMessageFixture(t)
	second, _ := readMessageFixture(t)
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	var folders []string
	for _, s := range []*server{first, second} {
		result, err := s.toolDigestSource(map[string]any{"talker": talker, "since_last": true, "include_media_paths": false})
		if err != nil {
			t.Fatal(err)
		}
		data := result.(map[string]any)
		if data["message_count"] != 4 {
			t.Fatalf("account inherited another account checkpoint: %#v", data)
		}
		folders = append(folders, data["folder"].(string))
	}
	if folders[0] == folders[1] {
		t.Fatal("accounts share a digest source directory")
	}
}

func TestConcurrentDigestsConsumeDisjointWindows(t *testing.T) {
	s, talker := readMessageFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	var wg sync.WaitGroup
	results := make(chan any, 2)
	errors := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			reader := &server{cfg: s.cfg, wcdbPath: s.wcdbPath, ok: true}
			result, err := reader.toolDigestSource(map[string]any{"talker": talker, "since_last": true, "limit": int64(2), "include_media_paths": false})
			if err != nil {
				errors <- err
				return
			}
			results <- result
		})
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	seen := map[string]bool{}
	for result := range results {
		path := result.(map[string]any)["source_json"].(string)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			t.Fatal(err)
		}
		for _, message := range payload.Messages {
			id := message["id"].(map[string]any)["server_id_str"].(string)
			if seen[id] {
				t.Fatalf("concurrent digests repeated message %s", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 4 {
		t.Fatalf("messages consumed = %d, want 4", len(seen))
	}
}

func TestCacheLockDoesNotExpireWhileOwnerLives(t *testing.T) {
	lock := t.TempDir()
	if err := writeCacheRefreshLockOwner(lock); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(lock, past, past); err != nil {
		t.Fatal(err)
	}
	if cacheRefreshLockStale(lock) {
		t.Fatal("live cache writer was considered stale after two hours")
	}
}

func TestFailedCacheRebuildPreservesPreviousIndex(t *testing.T) {
	s, root := liveSessionFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	paths, err := s.cachePaths()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "db_storage"), filepath.Join(root, "offline")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolCacheRebuild(map[string]any{}); err == nil {
		t.Fatal("expected unavailable source failure")
	}
	after, err := os.ReadFile(paths.IndexPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed rebuild lost the previous index: %v", err)
	}
}

func TestPrivateOutputRejectsSymlinkEscape(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WECHAT_CLI_STATE_DIR", state)
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(state, "digests")); err != nil {
		t.Skip(err)
	}
	if _, err := digestRoot(map[string]any{}); err == nil {
		t.Fatal("default digest path escaped via symlink")
	}
	if err := requirePrivateOutput(filepath.Join(state, "digests", "new"), "digests", false); err == nil {
		t.Fatal("explicit digest path escaped via symlink")
	}
	if err := requirePrivateOutput(external, "digests", true); err != nil {
		t.Fatalf("explicit external authorization rejected: %v", err)
	}
}

func TestDigestResumeAppliesOffsetOnlyToInitialWindow(t *testing.T) {
	s, talker := readMessageFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	args := map[string]any{"talker": talker, "since_last": true, "offset": 1, "limit": 1, "include_media_paths": false}
	for _, want := range []int64{2, 3, 4} {
		result, err := s.toolDigestSource(args)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(result.(map[string]any)["checkpoint"].(string))
		if err != nil {
			t.Fatal(err)
		}
		var cursor digestCursor
		if err := json.Unmarshal(b, &cursor); err != nil {
			t.Fatal(err)
		}
		if cursor.LocalID != want {
			t.Fatalf("offset skipped an incremental window: local_id=%d, want %d", cursor.LocalID, want)
		}
	}
}

func TestRetentionWaitsForDigestAndRechecksAge(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WECHAT_CLI_STATE_DIR", state)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	folder := filepath.Join(state, "digests", "chat-fixture")
	if err := saveDigestState(folder, digestCursor{LocalID: 1}); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-15 * 24 * time.Hour)
	if err := os.Chtimes(folder, past, past); err != nil {
		t.Fatal(err)
	}
	unlock, err := filelock.Acquire(digestLockPath(folder))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan any, 1)
	errors := make(chan error, 1)
	go func() {
		data, err := (&server{}).toolArchiveRetention(map[string]any{"apply": true})
		result <- data
		errors <- err
	}()
	select {
	case <-result:
		unlock()
		t.Fatal("retention moved a digest while its writer was active")
	case <-time.After(30 * time.Millisecond):
	}
	if err := saveDigestState(folder, digestCursor{LocalID: 2}); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	data := <-result
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	if moved := data.(map[string]any)["moved"].([]map[string]any); len(moved) != 0 {
		t.Fatal("retention ignored newly saved digest state")
	}
}

func TestArchiveCreatePreservesExistingSnapshotDirectory(t *testing.T) {
	s, _ := liveSessionFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	root, err := archiveBaseDir()
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "existing")
	manifest := filepath.Join(dst, "archive-manifest.json")
	if err := atomicPrivateWrite(manifest, []byte("prior manifest")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolArchiveCreate(map[string]any{"output": dst}); err == nil {
		t.Fatal("archive overwrote an existing snapshot directory")
	}
	if data, err := os.ReadFile(manifest); err != nil || string(data) != "prior manifest" {
		t.Fatal("previous manifest was modified")
	}
}

func TestArchiveCreateSerializesSameDestination(t *testing.T) {
	s, _ := liveSessionFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	root, err := archiveBaseDir()
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "concurrent")
	lockPath, err := archiveMutationLockPath(dst)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := filelock.Acquire(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.toolArchiveCreate(map[string]any{"output": dst})
		done <- err
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("archive writer bypassed the destination lock")
	case <-time.After(30 * time.Millisecond):
	}
	manifest := filepath.Join(dst, "archive-manifest.json")
	if err := atomicPrivateWrite(manifest, []byte("other writer completed")); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err == nil {
		t.Fatal("archive writer failed to recheck destination after waiting")
	}
	if data, err := os.ReadFile(manifest); err != nil || string(data) != "other writer completed" {
		t.Fatal("concurrent archive was overwritten")
	}
}

func TestArchiveDeleteWaitsForActiveWriter(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	root, err := archiveBaseDir()
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(root, "active-fixture")
	if err := atomicPrivateWrite(filepath.Join(folder, "archive-manifest.json"), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockManagedArchiveMutation(folder, "archive")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := (&server{}).toolArchiveDelete(map[string]any{"name": "active-fixture", "quarantine": true})
		done <- err
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("archive delete moved an active writer's folder")
	case <-time.After(30 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSafeArchiveNamePreservesUTF8ByteBoundary(t *testing.T) {
	for _, value := range []string{"a" + strings.Repeat("中", 50), "中" + strings.Repeat("😀", 40)} {
		name := safeArchiveName(value)
		if !utf8.ValidString(name) || len(name) > 120 || !strings.HasPrefix(value, name) {
			t.Fatalf("invalid truncated archive name: %q (%d bytes)", name, len(name))
		}
		b, err := json.Marshal(name)
		if err != nil {
			t.Fatal(err)
		}
		var roundtrip string
		if err := json.Unmarshal(b, &roundtrip); err != nil || roundtrip != name {
			t.Fatal("archive name changed after JSON serialization")
		}
	}
}

func TestRetentionRejectsManagedRootSymlinkEscape(t *testing.T) {
	for _, managed := range []string{"archives", "digests", filepath.Join("archives", ".quarantine")} {
		t.Run(managed, func(t *testing.T) {
			state, outside := t.TempDir(), t.TempDir()
			t.Setenv("WECHAT_CLI_STATE_DIR", state)
			t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
			folder := filepath.Join(outside, "unrelated")
			if err := atomicPrivateWrite(filepath.Join(folder, "important.txt"), []byte("preserve")); err != nil {
				t.Fatal(err)
			}
			past := time.Now().Add(-30 * 24 * time.Hour)
			if err := os.Chtimes(folder, past, past); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(state, managed)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Skip(err)
			}
			if _, err := (&server{}).toolArchiveRetention(map[string]any{"apply": true}); err == nil {
				t.Fatal("retention accepted an escaping managed root")
			}
			if data, err := os.ReadFile(filepath.Join(folder, "important.txt")); err != nil || string(data) != "preserve" {
				t.Fatal("retention changed external data")
			}
		})
	}
}

func TestRestoreRejectsDigestRootSymlinkEscape(t *testing.T) {
	state, outside := t.TempDir(), t.TempDir()
	t.Setenv("WECHAT_CLI_STATE_DIR", state)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	root, err := archiveBaseDir()
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(state, "digests", "chat-fixture")
	if err := saveDigestState(folder, digestCursor{LocalID: 1}); err != nil {
		t.Fatal(err)
	}
	quarantined, err := quarantineManagedFolder(root, folder, "digest")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(folder)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Dir(folder)); err != nil {
		t.Skip(err)
	}
	if _, err := (&server{}).toolArchiveRestore(map[string]any{"name": filepath.Base(quarantined)}); err == nil {
		t.Fatal("restore wrote through external digest symlink")
	}
	if _, err := os.Stat(quarantined); err != nil {
		t.Fatal("rejected restore moved its source")
	}
}
