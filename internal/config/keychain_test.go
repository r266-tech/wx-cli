package config

import (
	"encoding/json"
	"errors"
	"github.com/r266-tech/wx-cli/v2/internal/keystore"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKeychainRoundTripNeverRewritesPlaintext(t *testing.T) {
	t.Setenv("WECHAT_CLI_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	oldLoad, oldSave := runtimeKeyLoad, runtimeKeySave
	t.Cleanup(func() { runtimeKeyLoad, runtimeKeySave = oldLoad, oldSave })
	record := keystore.Record{SchemaVersion: 1, WxID: "fixture", DBRoot: "/fixture", Keys: map[string]string{"synthetic-salt": "synthetic-key"}, ImageKey: "synthetic-image", KeyEpoch: 8}
	runtimeKeyLoad = func(string, string) (*keystore.Record, error) { return &record, nil }
	runtimeKeySave = func(r keystore.Record) error { record = r; return nil }
	c := &Config{SchemaVersion: 3, Wxid: record.WxID, DBRoot: record.DBRoot, KeyStore: "keychain", KeyStoreRef: keystore.Account(record.DBRoot, record.WxID), KeyEpoch: 8}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil || !loaded.Ready() {
		t.Fatal("hydration failed", err)
	}
	if err := Save(loaded); err != nil {
		t.Fatal(err)
	}
	p, _ := Path()
	b, _ := os.ReadFile(p)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	for _, k := range []string{"keys", "image_key", "image_xor_key", "key"} {
		if _, ok := raw[k]; ok {
			t.Errorf("plaintext secret field %s persisted", k)
		}
	}
	record.KeyEpoch = 7
	if _, err := Load(); err == nil {
		t.Fatal("epoch rollback accepted")
	}
	runtimeKeyLoad = func(string, string) (*keystore.Record, error) { return nil, errors.New("secret error material") }
	if _, err := Load(); err == nil || err.Error() != "keychain runtime keys unavailable" {
		t.Fatal("failed read must be redacted and fail closed")
	}
}

func TestKeychainMigrationRejectsMismatchedReadback(t *testing.T) {
	oldLoad, oldSave := runtimeKeyLoad, runtimeKeySave
	t.Cleanup(func() { runtimeKeyLoad, runtimeKeySave = oldLoad, oldSave })
	runtimeKeySave = func(keystore.Record) error { return nil }
	runtimeKeyLoad = func(root, id string) (*keystore.Record, error) {
		return &keystore.Record{SchemaVersion: 1, WxID: id, DBRoot: root, Keys: map[string]string{"salt": "stale-value"}}, nil
	}
	cfg := &Config{SchemaVersion: 2, Wxid: "fixture", DBRoot: "/fixture", Keys: map[string]string{"salt": "new-value"}}
	if migrated, err := MigrateToKeychain(cfg); err == nil || migrated != nil {
		t.Fatal("migration discarded plaintext after mismatched Keychain readback")
	}
	if cfg.Keys["salt"] != "new-value" {
		t.Fatal("failed migration changed original config")
	}
}

func TestKeychainHydrationPreservesImageKeyEnvironmentOverride(t *testing.T) {
	t.Setenv("WECHAT_CLI_IMAGE_KEY", "explicit-fixture-image-key")
	cfg := &Config{Wxid: "fixture", DBRoot: "/fixture", KeyStore: "keychain", KeyStoreRef: keystore.Account("/fixture", "fixture")}
	err := hydrateRuntimeKeysWith(cfg, func(string, string) (*keystore.Record, error) {
		return &keystore.Record{SchemaVersion: 1, WxID: "fixture", DBRoot: "/fixture", Keys: map[string]string{"salt": "key"}, ImageKey: "stored-fixture-key"}, nil
	})
	if err != nil || cfg.ImageKey != "explicit-fixture-image-key" {
		t.Fatalf("image key override was discarded: %v", err)
	}
}

func TestPersistentKeychainMigrationSerializesConcurrentRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("WECHAT_CLI_CONFIG", path)
	if err := Save(&Config{SchemaVersion: 2, Wxid: "fixture", DBRoot: "/fixture", Keys: map[string]string{"old": "value"}}); err != nil {
		t.Fatal(err)
	}
	oldLoad, oldSave := runtimeKeyLoad, runtimeKeySave
	t.Cleanup(func() { runtimeKeyLoad, runtimeKeySave = oldLoad, oldSave })
	var mu sync.Mutex
	var stored []byte
	runtimeKeySave = func(record keystore.Record) error {
		mu.Lock()
		defer mu.Unlock()
		var err error
		stored, err = json.Marshal(record)
		return err
	}
	runtimeKeyLoad = func(string, string) (*keystore.Record, error) {
		mu.Lock()
		defer mu.Unlock()
		var record keystore.Record
		err := json.Unmarshal(stored, &record)
		return &record, err
	}
	backingUp := make(chan struct{})
	release := make(chan struct{})
	migrated := make(chan error, 1)
	go func() {
		_, err := MigratePersistentToKeychain(func(cfg *Config) error {
			close(backingUp)
			<-release
			return nil
		})
		migrated <- err
	}()
	<-backingUp
	mutating := make(chan struct{})
	updated := make(chan error, 1)
	go func() {
		updated <- Update(func(cfg *Config) error {
			close(mutating)
			cfg.Keys["new"] = "fresh-value"
			return nil
		})
	}()
	select {
	case <-mutating:
		t.Error("refresh entered while migration held the transaction")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-migrated; err != nil {
		t.Fatal(err)
	}
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil || cfg.KeyStore != "keychain" || cfg.Keys["new"] != "fresh-value" || cfg.Keys["old"] != "value" {
		t.Fatalf("concurrent refresh was lost: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(b), "fresh-value") || strings.Contains(string(b), `"keys"`) {
		t.Fatal("plaintext keys persisted after migration")
	}
}
func TestReferenceAloneIsNotReady(t *testing.T) {
	if (&Config{KeyStore: "keychain"}).Ready() {
		t.Fatal("reference is not key material")
	}
}

func TestKeychainAuthorizationValidatesAccountAndNeverWritesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("WECHAT_CLI_CONFIG", path)
	oldLoad, oldAuthorize := runtimeKeyLoad, runtimeKeyAuthorize
	t.Cleanup(func() { runtimeKeyLoad, runtimeKeyAuthorize = oldLoad, oldAuthorize })
	fixture := Config{SchemaVersion: 3, Wxid: "fixture", DBRoot: "/fixture", KeyStore: "keychain", KeyStoreRef: keystore.Account("/fixture", "fixture"), KeyEpoch: 9}
	before, _ := json.Marshal(fixture)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	runtimeKeyLoad = func(string, string) (*keystore.Record, error) { return nil, keystore.ErrInteractionRequired }
	metadata, err := LoadRuntimeKeyMetadata()
	if err != nil || metadata.Store != "keychain" {
		t.Fatal("diagnostic metadata needs no Keychain access", err)
	}
	if _, err := Load(); !errors.Is(err, keystore.ErrInteractionRequired) {
		t.Fatal("authorization cause lost", err)
	}
	calls := 0
	runtimeKeyAuthorize = func(dbRoot, wxid string) (*keystore.Record, error) {
		calls++
		return &keystore.Record{SchemaVersion: 1, DBRoot: dbRoot, WxID: wxid, Keys: map[string]string{"synthetic-salt": "synthetic-key"}, KeyEpoch: 9}, nil
	}
	loaded, err := LoadWithKeychainAuthorization()
	if err != nil || !loaded.Ready() || calls != 1 {
		t.Fatal("explicit authorization did not load", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("authorization changed the config")
	}
	fixture.KeyStoreRef = "different-account"
	invalid, _ := json.Marshal(fixture)
	if err := os.WriteFile(path, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWithKeychainAuthorization(); err == nil {
		t.Fatal("invalid reference authorized")
	}
	if calls != 1 {
		t.Fatal("OS authorization attempted for mismatched account")
	}
}

func TestKeychainAuthorizationFailureAndRollbackRemainClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("WECHAT_CLI_CONFIG", path)
	oldAuthorize := runtimeKeyAuthorize
	t.Cleanup(func() { runtimeKeyAuthorize = oldAuthorize })
	fixture := Config{SchemaVersion: 3, Wxid: "fixture", DBRoot: "/fixture", KeyStore: "keychain", KeyStoreRef: keystore.Account("/fixture", "fixture"), KeyEpoch: 9}
	before, _ := json.Marshal(fixture)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []error{keystore.ErrAuthorizationDenied, keystore.ErrItemNotFound, errors.New("untrusted secret detail")} {
		runtimeKeyAuthorize = func(string, string) (*keystore.Record, error) { return nil, failure }
		if c, err := LoadWithKeychainAuthorization(); err == nil || c != nil || strings.Contains(err.Error(), "secret detail") {
			t.Fatal("authorization failure leaked or opened", err)
		}
	}
	runtimeKeyAuthorize = func(string, string) (*keystore.Record, error) {
		return &keystore.Record{SchemaVersion: 1, DBRoot: fixture.DBRoot, WxID: fixture.Wxid, Keys: map[string]string{"synthetic-salt": "synthetic-key"}, KeyEpoch: 8}, nil
	}
	if _, err := LoadWithKeychainAuthorization(); err == nil {
		t.Fatal("authorization accepted an older epoch")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("failed authorization changed config")
	}
}
