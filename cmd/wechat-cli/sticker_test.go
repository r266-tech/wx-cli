package main

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

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
