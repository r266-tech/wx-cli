package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/r266-tech/wx-cli/v2/internal/config"
	"github.com/r266-tech/wx-cli/v2/internal/keystore"
)

func (s *server) toolKeychainStatus(a map[string]any) (any, error) {
	metadata, err := config.LoadRuntimeKeyMetadata()
	if err != nil {
		return nil, err
	}
	store := metadata.Store
	available := runtime.GOOS == "darwin" && keystore.Available()
	loaded := false
	var loadErr error
	if available && store == "keychain" {
		cfg, err := config.Load()
		loadErr = err
		loaded = err == nil && cfg.Ready()
	}
	data := map[string]any{
		"available":        available,
		"store":            store,
		"loaded":           loaded,
		"config_path":      configPathSafe(),
		"migration_needed": store == "config_legacy" && available,
	}
	if backups := keychainMigrationBackups(); len(backups) > 0 {
		data["plaintext_backups"] = len(backups)
		if loaded {
			data["next_action"] = "Plaintext key backups from an earlier migration remain; run keychain migrate to remove them now that Keychain access is verified."
			data["suggested_commands"] = []string{appName + " keychain migrate", appName + " keychain status"}
		}
	}
	if loadErr != nil {
		code := keychainDiagnosticCode(loadErr)
		data["code"] = code
		if code == "keychain_access_required" {
			advice := cliErrorAdvice(code, "", "keychain_status", "keychain status")
			data["next_action"] = advice.NextAction
			data["suggested_commands"] = advice.SuggestedCommands
		}
	}
	return data, nil
}

// isKeychainLoadError reports whether err comes from reading the Keychain
// runtime item, as opposed to a missing or malformed config.
func isKeychainLoadError(err error) bool {
	for _, known := range []error{keystore.ErrInteractionRequired, keystore.ErrAuthorizationDenied, keystore.ErrItemNotFound, keystore.ErrUnavailable} {
		if errors.Is(err, known) {
			return true
		}
	}
	return false
}

func keychainDiagnosticCode(err error) string {
	switch {
	case errors.Is(err, keystore.ErrInteractionRequired):
		return "keychain_access_required"
	case errors.Is(err, keystore.ErrAuthorizationDenied):
		return "keychain_authorization_denied"
	case errors.Is(err, keystore.ErrItemNotFound):
		return "keychain_item_missing"
	case errors.Is(err, keystore.ErrUnavailable):
		return "keychain_unavailable"
	default:
		return "keychain_record_invalid"
	}
}

func (s *server) toolKeychainAuthorize(a map[string]any) (any, error) {
	if strictReadOnlyMode() {
		return nil, fmt.Errorf("strict_read_only: keychain authorization can change macOS access state")
	}
	if !getBool(a, "interactive") {
		return nil, fmt.Errorf("keychain authorization requires explicit --interactive and a local macOS dialog")
	}
	if !keystore.Available() {
		return nil, keystore.ErrUnavailable
	}
	cfg, err := config.LoadWithKeychainAuthorization()
	if err != nil {
		return nil, err
	}
	return map[string]any{"store": "keychain", "loaded": cfg.Ready(), "status": "authorized", "key_count": len(cfg.Keys)}, nil
}

// toolKeychainHandoff runs on the old binary right before an update replaces
// it; see config.StageKeychainHandoff.
func (s *server) toolKeychainHandoff(a map[string]any) (any, error) {
	if strictReadOnlyMode() {
		return nil, fmt.Errorf("strict_read_only: keychain handoff writes local secret/config state")
	}
	if !keystore.Available() {
		return nil, keystore.ErrUnavailable
	}
	n, err := config.StageKeychainHandoff()
	if err != nil {
		return nil, fmt.Errorf("keychain handoff failed: %w", err)
	}
	return map[string]any{
		"status":      "staged",
		"key_count":   n,
		"next_action": "Replace the binary, then run any read (or keychain migrate) with the new one; it stores the keys in its own Keychain item and removes them from config.json.",
	}, nil
}

func (s *server) toolKeychainMigrate(a map[string]any) (any, error) {
	if strictReadOnlyMode() {
		return nil, fmt.Errorf("strict_read_only: keychain migration writes local secret/config state")
	}
	if !keystore.Available() {
		return nil, fmt.Errorf("macOS Keychain runtime store is unavailable on %s", runtime.GOOS)
	}
	return migrateKeysToKeychain()
}

// migrateKeysToKeychain moves plaintext runtime keys (as written by wxkey)
// into the Keychain and removes the plaintext copies once read back.
func migrateKeysToKeychain() (map[string]any, error) {
	backupDir, err := appStateDir()
	if err != nil {
		return nil, err
	}
	backupPath := filepath.Join(backupDir, "keychain-migration", time.Now().Format("20060102-150405.000000000")+"-config.json")
	result, err := config.MigratePersistentToKeychain(func(cfg *config.Config) error {
		oldBytes, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return atomicPrivateWrite(backupPath, append(oldBytes, '\n'))
	})
	if err != nil {
		return nil, fmt.Errorf("keychain migration failed: %w", err)
	}
	// Reaching here means the Keychain record was written and read back
	// identically (or was already loaded), so the pre-migration plaintext
	// copies are no longer a rollback path, only exposed secrets.
	removed, cleanupErr := removeKeychainMigrationBackups()
	data := map[string]any{
		"status":                    "already_migrated",
		"store":                     "keychain",
		"plaintext_backups_removed": removed,
	}
	if result.ResidueRemoved {
		data["plaintext_config_residue_removed"] = true
	}
	if result.Migrated {
		data["status"] = "migrated"
		data["key_count"] = result.KeyCount
		data["key_reference"] = result.Reference
	}
	if cleanupErr != nil {
		data["warnings"] = []string{"plaintext_backup_cleanup_failed"}
		data["next_action"] = "Remove the remaining files under " + keychainMigrationDir() + " manually; they contain database keys."
	}
	return data, nil
}

// autoMigrateLegacyKeys runs the Keychain migration without being asked when
// wxkey has just written plaintext keys, so agents never need a separate step.
// It never blocks reads: on failure the plaintext keys keep working.
func autoMigrateLegacyKeys(cfg *config.Config) bool {
	if cfg == nil || !cfg.Ready() || strictReadOnlyMode() ||
		runtime.GOOS != "darwin" || !keystore.Available() || os.Getenv("WECHAT_CLI_NO_KEYCHAIN_MIGRATE") == "1" {
		return false
	}
	if meta, err := config.LoadRuntimeKeyMetadata(); err != nil || meta.Store != "config_legacy" {
		return false
	}
	if _, err := migrateKeysToKeychain(); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] keys stay in config.json; automatic Keychain migration failed: %v\n", appName, err)
		return false
	}
	return true
}

func keychainMigrationDir() string {
	stateDir, err := appStateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(stateDir, "keychain-migration")
}

// keychainMigrationBackups lists regular backup files only; symlinks and
// anything else in the directory are left alone.
func keychainMigrationBackups() []string {
	dir := keychainMigrationDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), "-config.json") {
			continue
		}
		out = append(out, filepath.Join(dir, entry.Name()))
	}
	return out
}

func removeKeychainMigrationBackups() (int, error) {
	removed := 0
	var firstErr error
	for _, path := range keychainMigrationBackups() {
		if err := os.Remove(path); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	_ = os.Remove(keychainMigrationDir()) // only succeeds when empty
	return removed, firstErr
}

func configPathSafe() string {
	path, err := config.Path()
	if err != nil {
		return ""
	}
	return path
}
