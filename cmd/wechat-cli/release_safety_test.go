package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDigestStateUsesStableTalkerIdentity(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	a := map[string]any{"talker": "fixture@chatroom"}
	root, err := digestRoot(a)
	if err != nil {
		t.Fatal(err)
	}
	cursor := digestCursor{Timestamp: 100, LocalID: 7, ServerID: "9"}
	if err := saveDigestState(filepath.Join(root, digestFolderName("fixture@chatroom")), cursor); err != nil {
		t.Fatal(err)
	}
	got, err := loadDigestState(a)
	if err != nil || got != cursor {
		t.Fatal("cursor not recovered", err)
	}
}
func TestRetentionDryRunDoesNotWrite(t *testing.T) {
	state := filepath.Join(t.TempDir(), "absent-state")
	t.Setenv("WECHAT_CLI_STATE_DIR", state)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	if _, err := (&server{}).toolArchiveRetention(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("dry run wrote state")
	}
	for _, fn := range []func(map[string]any) (any, error){(&server{}).toolArchiveDelete, (&server{}).toolArchiveRestore} {
		if _, err := fn(map[string]any{"name": "fixture", "quarantine": true}); err == nil {
			t.Fatal("strict write accepted")
		}
	}
}
