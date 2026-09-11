package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"time"

	"github.com/r266-tech/wx-cli/internal/config"
	"github.com/r266-tech/wx-cli/internal/keystore"
)

func (s *server) toolKeychainStatus(a map[string]any) (any, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	store := "missing"
	if cfg.KeyStore == "keychain" {
		store = "keychain"
	} else if len(cfg.Keys) > 0 {
		store = "config_legacy"
	}
	available := runtime.GOOS == "darwin" && keystore.Available()
	loaded := false
	if available && cfg.DBRoot != "" && cfg.Wxid != "" {
		_, loadErr := keystore.Load(cfg.DBRoot, cfg.Wxid)
		loaded = loadErr == nil
	}
	return map[string]any{
		"available":        available,
		"store":            store,
		"loaded":           loaded,
		"config_path":      configPathSafe(),
		"migration_needed": store == "config_legacy" && available,
	}, nil
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
	if cfg.KeyStore == "keychain" && len(cfg.Keys) == 0 {
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
