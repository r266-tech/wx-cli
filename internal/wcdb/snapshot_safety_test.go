package wcdb

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"
)

func TestBackupUsesPrivateUniqueTemporaryFile(t *testing.T) {
	oldOpen, oldClose, oldExec := sqlite3_open_v2, sqlite3_close_v2, sqlite3_exec
	oldInit, oldStep, oldFinish := sqlite3_backup_init, sqlite3_backup_step, sqlite3_backup_finish
	t.Cleanup(func() {
		sqlite3_open_v2, sqlite3_close_v2, sqlite3_exec = oldOpen, oldClose, oldExec
		sqlite3_backup_init, sqlite3_backup_step, sqlite3_backup_finish = oldInit, oldStep, oldFinish
	})
	root := t.TempDir()
	dst := filepath.Join(root, "archive.db")
	legacyTemp := dst + ".tmp"
	if err := os.WriteFile(legacyTemp, []byte("other writer"), 0o600); err != nil {
		t.Fatal(err)
	}
	var opened string
	sqlite3_open_v2 = func(path string, handle *uintptr, _ int32, _ *byte) int32 {
		opened = path
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("snapshot was not reserved before SQLite open: %v", err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("plaintext file mode = %o", info.Mode().Perm())
		}
		if err := os.WriteFile(path, []byte("synthetic snapshot"), 0o666); err != nil {
			t.Fatal(err)
		}
		*handle = 2
		return SQLITE_OK
	}
	sqlite3_close_v2 = func(uintptr) int32 { return SQLITE_OK }
	sqlite3_exec = func(uintptr, string, uintptr, uintptr, *unsafe.Pointer) int32 { return SQLITE_OK }
	sqlite3_backup_init = func(uintptr, string, uintptr, string) uintptr { return 3 }
	sqlite3_backup_step = func(uintptr, int32) int32 { return SQLITE_DONE }
	sqlite3_backup_finish = func(uintptr) int32 { return SQLITE_OK }
	if err := (&DB{handle: 1}).BackupTo(dst); err != nil {
		t.Fatal(err)
	}
	if opened == legacyTemp {
		t.Fatal("backup used a shared temporary path")
	}
	if b, err := os.ReadFile(legacyTemp); err != nil || string(b) != "other writer" {
		t.Fatalf("backup changed another writer's temporary file: %q, %v", b, err)
	}
	if _, err := os.Stat(opened); !os.IsNotExist(err) {
		t.Fatalf("temporary file was not removed: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("final snapshot is not private: %v, %v", info, err)
	}
	// A failed publication must also clean the plaintext temporary database.
	if err := os.Mkdir(filepath.Join(root, "blocked"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (&DB{handle: 1}).BackupTo(filepath.Join(root, "blocked")); err == nil {
		t.Fatal("publishing over a directory should fail")
	}
	if _, err := os.Stat(opened); !os.IsNotExist(err) {
		t.Fatalf("failed publication leaked temporary plaintext: %v", err)
	}
}

func TestTextColumnPreservesEmbeddedNUL(t *testing.T) {
	oldType, oldText, oldBytes := sqlite3_column_type, sqlite3_column_text, sqlite3_column_bytes
	t.Cleanup(func() {
		sqlite3_column_type, sqlite3_column_text, sqlite3_column_bytes = oldType, oldText, oldBytes
	})
	value := []byte("before\x00after\x00")
	sqlite3_column_type = func(uintptr, int32) int32 { return COL_TEXT }
	sqlite3_column_text = func(uintptr, int32) unsafe.Pointer { return unsafe.Pointer(&value[0]) }
	sqlite3_column_bytes = func(uintptr, int32) int32 { return int32(len(value) - 1) }
	if got := readColumn(1, 0); got != "before\x00after" {
		t.Fatalf("text was truncated: %q", got)
	}
}
