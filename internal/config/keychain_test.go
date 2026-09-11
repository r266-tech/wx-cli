package config

import (
	"encoding/json"
	"errors"
	"github.com/r266-tech/wx-cli/v2/internal/keystore"
	"os"
	"path/filepath"
	"testing"
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
func TestReferenceAloneIsNotReady(t *testing.T) {
	if (&Config{KeyStore: "keychain"}).Ready() {
		t.Fatal("reference is not key material")
	}
}
