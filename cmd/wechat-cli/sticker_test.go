package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/config"
	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

func stickerRow(sum, url string) wcdb.Row {
	return wcdb.Row{"kind_name": "sticker", "message_content_parsed": map[string]any{"md5": sum, "cdn_url": url}}
}

func TestStickerFetchVerifiesAndCaches(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	gif := append([]byte("GIF89a"), make([]byte, 64)...)
	sum := md5.Sum(gif)
	good := hex.EncodeToString(sum[:])
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://evil.example/x", http.StatusFound)
			return
		}
		_, _ = w.Write(gif)
	}))
	defer srv.Close()
	prev := stickerHostAllowed
	stickerHostAllowed = func(string) bool { return true }
	defer func() { stickerHostAllowed = prev }()

	s := &server{}
	rows := []wcdb.Row{
		stickerRow(good, srv.URL+"/ok"),
		stickerRow("0123456789abcdef0123456789abcdef", srv.URL+"/ok"),
		stickerRow("fedcba9876543210fedcba9876543210", srv.URL+"/redirect"),
	}
	s.attachStickerImages(rows)
	path := rowString(rows[0], stickerImagePathField)
	if path == "" || rows[1][stickerImagePathField] != nil || rows[2][stickerImagePathField] != nil {
		t.Fatalf("paths: %v / %v / %v", rows[0][stickerImagePathField], rows[1][stickerImagePathField], rows[2][stickerImagePathField])
	}
	if b, err := os.ReadFile(path); err != nil || string(b[:6]) != "GIF89a" {
		t.Fatalf("cached sticker unreadable: %v", err)
	}
	images, _ := agentStickerImages(rows[0])
	if len(images) != 1 || images[0]["sticker"] != true {
		t.Fatalf("agent images = %v", images)
	}
	before := hits.Load()
	again := []wcdb.Row{stickerRow(good, srv.URL+"/ok"), stickerRow("0123456789abcdef0123456789abcdef", srv.URL+"/ok")}
	s.attachStickerImages(again)
	if hits.Load() != before || rowString(again[0], stickerImagePathField) != path {
		t.Fatalf("second read must use the cache and the failure marker, hits %d -> %d", before, hits.Load())
	}
}

func TestStickerHostAllowlist(t *testing.T) {
	for host, want := range map[string]bool{
		"vweixinf.tc.qq.com": true, "wxapp.tc.qq.com": true, "mmbiz.qpic.cn": true, "snsvideo.c2c.wechat.com": true,
		"evil.example": false, "qq.com.evil.example": false, "127.0.0.1": false, "localhost": false,
	} {
		if got := defaultStickerHostAllowed(host); got != want {
			t.Errorf("%s: got %v want %v", host, got, want)
		}
	}
}

func TestStickerFetchOffUnderStrictReadOnly(t *testing.T) {
	t.Setenv("WECHAT_CLI_STICKER_FETCH", "off")
	if stickerFetchEnabled() {
		t.Fatal("env switch must disable fetching")
	}
	if _, warnings := agentStickerImages(wcdb.Row{"kind_name": "sticker"}); len(warnings) != 1 || warnings[0] != "sticker_fetch_disabled" {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestStickerReadFromWeChatCache(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	docs := t.TempDir()
	root := filepath.Join(docs, "xwechat_files", "wxid_abc123_1f2e")
	kv := filepath.Join(docs, "app_data", "net", "kvcomm")
	if err := os.MkdirAll(kv, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kv, "key_123456789_1.statistic"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	encrypt := func(plain []byte) []byte {
		key := md5.Sum([]byte("123456789wxid_abc123EMOTICON"))
		pad := aes.BlockSize - len(plain)%aes.BlockSize
		buf := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
		block, _ := aes.NewCipher(key[:])
		cipher.NewCBCEncrypter(block, key[:]).CryptBlocks(buf, buf)
		return buf
	}
	place := func(rel string, plain []byte) string {
		sum := md5.Sum(plain)
		name := hex.EncodeToString(sum[:])
		dir := filepath.Join(root, rel, name[:2])
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), encrypt(plain), 0o600); err != nil {
			t.Fatal(err)
		}
		return name
	}
	gif := append([]byte("GIF89a"), bytes.Repeat([]byte{7}, 100)...)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{9}, 50)...)
	gifSum := place(filepath.Join("cache", "2026-11", "Emoticon"), gif)
	pngSum := place(filepath.Join("business", "emoticon", "Persist"), png)
	// wxgf entries are WeChat's HEVC re-encoding: the md5 does not match, so they are skipped.
	wxgf := append([]byte("wxgf"), bytes.Repeat([]byte{1}, 40)...)
	wxgfName := place(filepath.Join("cache", "2026-11", "Emoticon"), wxgf)
	t.Setenv("WECHAT_CLI_STICKER_FETCH", "off")

	s := &server{cfg: &config.Config{DBRoot: root}}
	if got := s.localStickerImage(gifSum); !bytes.Equal(got, gif) {
		t.Fatalf("gif from cache/Emoticon not decrypted")
	}
	rows := []wcdb.Row{stickerRow(pngSum, ""), stickerRow(gifSum, "")}
	s.attachStickerImages(rows)
	for i, want := range []string{".png", ".gif"} {
		if path := rowString(rows[i], stickerImagePathField); filepath.Ext(path) != want {
			t.Fatalf("row %d path = %q, want %s", i, path, want)
		}
	}
	if s.localStickerImage(wxgfName) != nil {
		t.Fatalf("wxgf bytes must not be served as the sticker")
	}
}

func TestStickerTransientErrorIsRetried(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	prev := stickerHostAllowed
	stickerHostAllowed = func(string) bool { return true }
	defer func() { stickerHostAllowed = prev }()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused: a network error, not a verdict on the sticker
	s := &server{}
	sum := hex.EncodeToString(make([]byte, 16))
	s.attachStickerImages([]wcdb.Row{stickerRow(sum, url)})
	dir, _ := s.stickerCacheDir()
	if stickerRecentlyFailed(dir, sum) {
		t.Fatalf("a network error must not suppress retries for %s", stickerNegativeTTL)
	}
}
