package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/config"
	"github.com/r266-tech/wx-cli/v2/internal/keystore"
)

func TestKeychainAuthorizeRequiresExplicitInteraction(t *testing.T) {
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	s := &server{}
	for _, args := range []map[string]any{nil, {"interactive": false}} {
		if _, err := s.toolKeychainAuthorize(args); err == nil || !strings.Contains(err.Error(), "explicit --interactive") {
			t.Fatal("an implicit request must not prompt", err)
		}
	}
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	if _, err := s.toolKeychainAuthorize(map[string]any{"interactive": true}); err == nil || !strings.Contains(err.Error(), "strict_read_only") {
		t.Fatal("strict read-only allowed authorization", err)
	}
}

func TestKeychainDiagnosticKeepsKnownCauseAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{keystore.ErrInteractionRequired, "keychain_access_required"},
		{keystore.ErrAuthorizationDenied, "keychain_authorization_denied"},
		{keystore.ErrItemNotFound, "keychain_item_missing"},
		{keystore.ErrUnavailable, "keychain_unavailable"},
		{errors.New("private data"), "keychain_record_invalid"},
	} {
		if got := keychainDiagnosticCode(fmt.Errorf("wrapped: %w", tc.err)); got != tc.code {
			t.Errorf("code=%q, want %q", got, tc.code)
		}
	}
	advice := cliErrorAdvice("keychain_access_required", "", "chat_timeline", "timeline")
	// The unattended route comes first; the dialog route stays available.
	if len(advice.SuggestedCommands) == 0 || advice.SuggestedCommands[0] != readOSBootstrapCommand() {
		t.Fatal("unattended recovery command must come first")
	}
	hasDialog := false
	for _, c := range advice.SuggestedCommands {
		hasDialog = hasDialog || c == appName+" keychain authorize --interactive"
	}
	if !hasDialog {
		t.Fatal("interactive recovery command missing")
	}
}

func TestKeychainMaintenanceToolsDescribeLocalWrites(t *testing.T) {
	for _, name := range []string{"keychain_status", "keychain_migrate", "keychain_authorize"} {
		if !toolInProfile(name, "maintenance") || toolInProfile(name, "assistant") {
			t.Errorf("incorrect profile for %s", name)
		}
	}
	for _, name := range []string{"keychain_migrate", "keychain_authorize"} {
		if toolLocalWriteMode(name) != "required" || toolStrictReadOnlyBehavior(name) != "blocked" {
			t.Errorf("incorrect write metadata for %s", name)
		}
	}
	if name, ok := callableToolNameForTarget("keychain authorize"); !ok || name != "keychain_authorize" {
		t.Fatal("authorize command schema missing")
	}
}

func TestKeychainMigrationBackupCleanupRemovesOnlyRegularBackups(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WECHAT_CLI_STATE_DIR", state)
	dir := filepath.Join(state, "keychain-migration")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-config.json")
	for _, path := range []string{filepath.Join(dir, "20260911-151704-config.json"), filepath.Join(dir, "20260911-152253-config.json"), filepath.Join(dir, "notes.txt"), outside} {
		if err := os.WriteFile(path, []byte(`{"keys":{"salt":"secret"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked-config.json")); err != nil {
		t.Fatal(err)
	}
	if got := len(keychainMigrationBackups()); got != 2 {
		t.Fatalf("backups = %d, want 2 regular backup files", got)
	}
	removed, err := removeKeychainMigrationBackups()
	if err != nil || removed != 2 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if len(keychainMigrationBackups()) != 0 {
		t.Fatal("backup files remain")
	}
	for _, keep := range []string{filepath.Join(dir, "notes.txt"), outside} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("cleanup touched %s: %v", keep, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "linked-config.json")); err != nil {
		t.Fatal("cleanup followed or removed a symlink")
	}
}

func TestAutoMigrateLegacyKeysSkipsWhenNothingToDo(t *testing.T) {
	t.Setenv("WECHAT_CLI_NO_KEYCHAIN_MIGRATE", "")
	ready := &config.Config{Wxid: "wxid_a", DBRoot: "/x", Keys: map[string]string{"s": "k"}}
	if autoMigrateLegacyKeys(nil) || autoMigrateLegacyKeys(&config.Config{}) {
		t.Fatal("must not migrate without keys")
	}
	already := *ready
	already.KeyStore = "keychain"
	if autoMigrateLegacyKeys(&already) {
		t.Fatal("must not migrate a Keychain-backed config")
	}
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	if autoMigrateLegacyKeys(ready) {
		t.Fatal("strict read-only must not write the Keychain")
	}
}

func TestToolErrorCodeReadsMachinePrefix(t *testing.T) {
	for msg, want := range map[string]string{
		"db_keys_missing: no schema-2 DB keys cached":        "db_keys_missing",
		"chat_ambiguous: \"维尼\" matches a (wxid_a, private)": "chat_ambiguous",
		"chat_not_found: no chat or contact matches \"x\"":   "chat_not_found",
		"invalid_argument: \"x\" is a folder session":        "invalid_argument",
		"message_cursor_stale: original message is gone":     "message_cursor_stale",
		"message_anchor_ambiguous: local_id repeats":         "message_anchor_ambiguous",
		"table Msg_x not found in 8 message shards":          "tool_error",
		"db_keys_missing without colon is not a code":        "tool_error",
	} {
		if got := toolErrorCode(errors.New(msg)); got != want {
			t.Errorf("toolErrorCode(%q)=%q, want %q", msg, got, want)
		}
	}
}

func TestKeychainLoadErrorIsRecognisedThroughWrapping(t *testing.T) {
	if !isKeychainLoadError(fmt.Errorf("keychain runtime keys unavailable: %w", keystore.ErrAuthorizationDenied)) {
		t.Fatal("wrapped Keychain denial not recognised")
	}
	if isKeychainLoadError(errors.New("config.json: unexpected end of JSON input")) {
		t.Fatal("config parse error must not be reported as a Keychain problem")
	}
}

func TestEveryHelpCommandHasDescription(t *testing.T) {
	for _, c := range cliCommandSpecs {
		if strings.TrimSpace(c.Description) == "" {
			t.Errorf("command %q has no description; agents read help to pick a command", c.Command)
		}
	}
}
