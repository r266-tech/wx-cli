package keystore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

type Record struct {
	SchemaVersion int               `json:"schema_version"`
	WxID          string            `json:"wxid"`
	DBRoot        string            `json:"db_root"`
	Keys          map[string]string `json:"keys"`
	ImageKey      string            `json:"image_key,omitempty"`
	ImageXORKey   *int              `json:"image_xor_key,omitempty"`
	KeyEpoch      int64             `json:"key_epoch,omitempty"`
	UpdatedAt     string            `json:"updated_at"`
}

const service = "com.r266.wechat-cli.runtime-keys.v1"

func Account(dbRoot, wxid string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(dbRoot) + "\x00" + strings.TrimSpace(wxid)))
	return hex.EncodeToString(h[:])
}

var ErrUnavailable = errors.New("keychain runtime store is only available on macOS")

// These errors contain no account or key material and may cross the CLI boundary.
var ErrInteractionRequired = errors.New("keychain access requires macOS authorization")
var ErrItemNotFound = errors.New("keychain runtime item is missing")
var ErrAuthorizationDenied = errors.New("keychain authorization was denied or cancelled")
