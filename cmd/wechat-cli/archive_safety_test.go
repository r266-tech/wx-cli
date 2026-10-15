package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

func TestArchiveEncryptedFixtureProducesPrivateReadableSnapshots(t *testing.T) {
	s, _ := liveSessionFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	result, err := s.toolArchiveCreate(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	archive := result.(map[string]any)
	if archive["failed"] != 0 || archive["succeeded"] != 2 {
		t.Fatalf("archive result = %#v", archive)
	}
	root := archive["archive_root"].(string)
	validated, err := s.toolArchiveValidate(map[string]any{"input": root})
	if err != nil || validated.(map[string]any)["valid"] != true {
		t.Fatalf("archive validation = %#v, %v", validated, err)
	}
	path := filepath.Join(root, "session", "session.db")
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("plaintext snapshot permissions = %v, %v", info, err)
	}
	db, err := wcdb.OpenPlain(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT count(*) AS n FROM SessionTable")
	if err != nil || len(rows) != 1 || rows[0]["n"] != int64(4) {
		t.Fatalf("plaintext snapshot rows = %#v, %v", rows, err)
	}
}

func TestDigestIncrementalCheckpointUsesTimelineOrder(t *testing.T) {
	s, talker := readMessageFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	path := filepath.Join(s.cfg.DBRoot, "db_storage", "message", "message_0.db")
	db, err := wcdb.OpenWithKeyMapWritable(path, s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	// The oldest message in timeline order has the newest wall-clock time.
	err = db.Exec("UPDATE " + quoteIdent("Msg_"+talkerHash(talker)) + " SET create_time=10000 WHERE local_id=1")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	args := scopedDigestArgs(map[string]any{"talker": talker, "since_last": true, "limit": int64(2), "include_media_paths": false}, s.cfg)
	for _, want := range []int64{2, 4} {
		result, err := s.toolDigestSource(args)
		if err != nil {
			t.Fatal(err)
		}
		if got := result.(map[string]any)["message_count"]; got != 2 {
			t.Fatalf("digest count = %v, want 2", got)
		}
		state, err := loadDigestState(args)
		if err != nil || state.LocalID != want {
			t.Fatalf("digest checkpoint = %#v, %v; want local_id %d", state, err, want)
		}
	}
	result, err := s.toolDigestSource(args)
	if err != nil || result.(map[string]any)["message_count"] != 0 {
		t.Fatalf("completed digest repeated messages: %#v, %v", result, err)
	}
}

func TestDigestResumesLegacyTimestampCheckpoint(t *testing.T) {
	s, talker := readMessageFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	args := scopedDigestArgs(map[string]any{"talker": talker, "since_last": true, "include_media_paths": false}, s.cfg)
	folder, err := digestStateFolder(args)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveDigestState(folder, digestCursor{Timestamp: 300}); err != nil {
		t.Fatal(err)
	}
	result, err := s.toolDigestSource(args)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any)["message_count"]; got != 2 {
		t.Fatalf("timestamp-only checkpoint was ignored: message_count = %v, want 2", got)
	}
}

func TestArchiveQuarantineRestorePreservesOriginalName(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	root, err := archiveBaseDir()
	if err != nil {
		t.Fatal(err)
	}
	name := "20261001-123456-聊天素材"
	src := filepath.Join(root, name)
	if err := atomicPrivateWrite(filepath.Join(src, "archive-manifest.json"), []byte(`{"schema_version":1}`)); err != nil {
		t.Fatal(err)
	}
	deleted, err := (&server{}).toolArchiveDelete(map[string]any{"name": name, "quarantine": true})
	if err != nil {
		t.Fatal(err)
	}
	quarantined := deleted.(map[string]any)["to"].(string)
	restored, err := (&server{}).toolArchiveRestore(map[string]any{"name": filepath.Base(quarantined)})
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.(map[string]any)["to"]; got != src {
		t.Fatalf("restore destination = %v, want %s", got, src)
	}
	if _, err := os.Stat(quarantined + ".origin.json"); !os.IsNotExist(err) {
		t.Fatalf("origin receipt was not cleaned: %v", err)
	}
}

func TestRetentionDigestRestoresToDigestRoot(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WECHAT_CLI_STATE_DIR", state)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	src := filepath.Join(state, "digests", "chat-synthetic")
	if err := atomicPrivateWrite(filepath.Join(src, "state.json"), []byte(`{"last_timestamp":10}`)); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-15 * 24 * time.Hour)
	if err := os.Chtimes(src, past, past); err != nil {
		t.Fatal(err)
	}
	result, err := (&server{}).toolArchiveRetention(map[string]any{"apply": true})
	if err != nil {
		t.Fatal(err)
	}
	moved := result.(map[string]any)["moved"].([]map[string]any)
	if len(moved) != 1 {
		t.Fatalf("moved = %#v", moved)
	}
	name := filepath.Base(moved[0]["to"].(string))
	restored, err := (&server{}).toolArchiveRestore(map[string]any{"name": name})
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.(map[string]any)["to"]; got != src {
		t.Fatalf("digest restored to wrong root: %v", got)
	}
	if got, err := os.ReadFile(filepath.Join(src, "state.json")); err != nil || string(got) != `{"last_timestamp":10}` {
		t.Fatalf("restored digest state = %q, %v", got, err)
	}
}

func TestLegacyQuarantineRestoresExactNameAndKind(t *testing.T) {
	for _, kind := range []string{"archive", "digest"} {
		t.Run(kind, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("WECHAT_CLI_STATE_DIR", state)
			t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
			name := "fixture-original-name"
			quarantined := filepath.Join(state, "archives", ".quarantine", "20261015-123456-"+name)
			marker := "archive-manifest.json"
			if kind == "digest" {
				marker = "state.json"
			}
			if err := atomicPrivateWrite(filepath.Join(quarantined, marker), []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			result, err := (&server{}).toolArchiveRestore(map[string]any{"name": filepath.Base(quarantined)})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.(map[string]any)["to"]; got != filepath.Join(state, kind+"s", name) {
				t.Fatalf("legacy restore destination = %v", got)
			}
		})
	}
}

func TestArchiveValidationRejectsIncompleteAndUnreadableRecords(t *testing.T) {
	for _, tc := range []struct {
		name    string
		records []map[string]any
		valid   bool
	}{
		{"empty", nil, false},
		{"failed", []map[string]any{{"path": "data.db", "status": "error"}}, false},
		{"directory", []map[string]any{{"path": "directory", "status": "ok", "sha256": fmt.Sprintf("%x", sha256.Sum256(nil))}}, false},
		{"valid", []map[string]any{{"path": "data.db", "status": "ok", "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("fixture")))}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "data.db"), []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(map[string]any{"schema_version": 1, "records": tc.records})
			if err != nil {
				t.Fatal(err)
			}
			if err := atomicPrivateWrite(filepath.Join(root, "archive-manifest.json"), b); err != nil {
				t.Fatal(err)
			}
			result, err := (&server{}).toolArchiveValidate(map[string]any{"input": root})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.(map[string]any)["valid"]; got != tc.valid {
				t.Fatalf("valid = %v, want %v", got, tc.valid)
			}
		})
	}
}
