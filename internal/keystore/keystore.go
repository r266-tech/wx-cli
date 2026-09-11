package keystore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"runtime"
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
func Available() bool { return runtime.GOOS == "darwin" }

var ErrUnavailable = errors.New("keychain runtime store is only available on macOS")
