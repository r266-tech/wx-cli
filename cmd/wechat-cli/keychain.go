package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
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
	if loadErr != nil {
		code := keychainDiagnosticCode(loadErr)
		data["code"] = code
		if code == "keychain_access_required" {
			data["next_action"] = "Run keychain authorize --interactive and approve access in the macOS dialog, then retry status."
			data["suggested_commands"] = []string{appName + " keychain authorize --interactive", appName + " keychain status"}
		}
	}
	return data, nil
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

func (s *server) toolKeychainMigrate(a map[string]any) (any, error) {
	if strictReadOnlyMode() {
		return nil, fmt.Errorf("strict_read_only: keychain migration writes local secret/config state")
	}
	if !keystore.Available() {
		return nil, fmt.Errorf("macOS Keychain runtime store is unavailable on %s", runtime.GOOS)
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.KeyStore == "keychain" {
		return map[string]any{"status": "already_migrated", "store": "keychain"}, nil
	}
	if len(cfg.Keys) == 0 {
		return nil, fmt.Errorf("legacy config has no runtime keys to migrate")
	}
	backupDir, err := appStateDir()
	if err != nil {
		return nil, err
	}
	backupPath := filepath.Join(backupDir, "keychain-migration", time.Now().Format("20060102-150405")+"-config.json")
	oldBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := atomicPrivateWrite(backupPath, append(oldBytes, '\n')); err != nil {
		return nil, err
	}
	migrated, err := config.MigrateToKeychain(cfg)
	if err != nil {
		return nil, fmt.Errorf("keychain migration failed: %w", err)
	}
	if err := config.Save(migrated); err != nil {
		_ = config.Save(cfg)
		return nil, fmt.Errorf("keychain migration config update failed; restored legacy config: %w", err)
	}
	verified, err := config.Load()
	if err != nil || !verified.Ready() || verified.KeyStore != "keychain" {
		_ = config.Save(cfg)
		return nil, fmt.Errorf("keychain migration verification failed; restored legacy config")
	}
	return map[string]any{
		"status":        "migrated",
		"store":         "keychain",
		"backup":        backupPath,
		"key_count":     len(cfg.Keys),
		"key_reference": migrated.KeyStoreRef,
	}, nil
}

func configPathSafe() string {
	path, err := config.Path()
	if err != nil {
		return ""
	}
	return path
}
