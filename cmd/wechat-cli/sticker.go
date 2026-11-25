package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

// Stickers are not stored as readable files. A sticker is read, in order,
// from wechat-cli's media cache, from WeChat's own encrypted emoticon cache
// (sticker_local.go), and last from the Tencent CDN URL in the message XML or
// WeChat's emoticon table, the same one WeChat downloads it from. Every
// source must produce bytes that hash to the sticker md5, and the result is
// kept in the media cache so later reads are local.
//
// The URL comes from the sender's message, so only Tencent hosts are
// contacted and redirects are not followed; WECHAT_CLI_STICKER_FETCH=off and
// --strict-read-only stop all network access.

const (
	stickerMaxBytes       = 32 << 20
	stickerFetchBudget    = 20 * time.Second
	stickerFetchPerCall   = 24
	stickerFetchWorkers   = 4
	stickerNegativeTTL    = 6 * time.Hour
	stickerImagePathField = "sticker_image_path"
)

var (
	stickerMD5RE        = regexp.MustCompile(`^[0-9a-f]{32}$`)
	stickerAllowedHosts = []string{".qq.com", ".qpic.cn", ".wechat.com"}
	// stickerHTTPClient is swapped in tests. Each fetch is bounded by the
	// per-call budget, so large animated stickers are not cut off early.
	stickerHTTPClient = &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// stickerHostAllowed is swapped in tests to reach a local server.
	stickerHostAllowed = defaultStickerHostAllowed
)

func defaultStickerHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if net.ParseIP(host) != nil {
		return false
	}
	for _, suffix := range stickerAllowedHosts {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func stickerFetchEnabled() bool {
	return !strictReadOnlyMode() && !strings.EqualFold(strings.TrimSpace(os.Getenv("WECHAT_CLI_STICKER_FETCH")), "off")
}

func (s *server) stickerCacheDir() (string, error) {
	dir, err := s.mediaDecodeCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "stickers"), nil
}

type stickerJob struct {
	row wcdb.Row
	md5 string
	url string
}

// attachStickerImages sets sticker_image_path on sticker rows whose image is
// cached or can be fetched within this call's budget.
func (s *server) attachStickerImages(rows []wcdb.Row) {
	dir, err := s.stickerCacheDir()
	if err != nil {
		return
	}
	// A sticker sent from a phone often carries only its md5; the URL is in
	// another message with the same md5 or in WeChat's own emoticon table.
	urls := map[string]string{}
	var missing []string
	for _, row := range rows {
		if rowString(row, "kind_name") != "sticker" {
			continue
		}
		p, _ := row["message_content_parsed"].(map[string]any)
		sum := strings.ToLower(strings.TrimSpace(stringMapValue(p, "md5")))
		if !stickerMD5RE.MatchString(sum) {
			continue
		}
		if path := cachedStickerPath(dir, sum); path != "" {
			row[stickerImagePathField] = path
			continue
		}
		if data := s.localStickerImage(sum); data != nil {
			if path, err := writeMediaCacheFile(filepath.Join(dir, sum+"."+stickerImageExt(data)), data); err == nil {
				row[stickerImagePathField] = path
				continue
			}
		}
		if raw := strings.TrimSpace(stringMapValue(p, "cdn_url")); raw != "" {
			urls[sum] = raw
		} else if _, seen := urls[sum]; !seen {
			urls[sum] = ""
			missing = append(missing, sum)
		}
	}
	if len(urls) == 0 || !stickerFetchEnabled() {
		return
	}
	if len(missing) > 0 {
		for sum, raw := range s.emoticonTableURLs(missing) {
			if urls[sum] == "" {
				urls[sum] = raw
			}
		}
	}
	var jobs []stickerJob
	for _, row := range rows {
		if rowString(row, "kind_name") != "sticker" || row[stickerImagePathField] != nil {
			continue
		}
		p, _ := row["message_content_parsed"].(map[string]any)
		sum := strings.ToLower(strings.TrimSpace(stringMapValue(p, "md5")))
		raw := urls[sum]
		if raw == "" || len(jobs) >= stickerFetchPerCall || stickerRecentlyFailed(dir, sum) {
			continue
		}
		delete(urls, sum)
		jobs = append(jobs, stickerJob{row: row, md5: sum, url: raw})
	}
	if len(jobs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stickerFetchBudget)
	defer cancel()
	paths := make(map[string]string, len(jobs))
	var mu sync.Mutex
	work := make(chan stickerJob)
	var wg sync.WaitGroup
	for i := 0; i < stickerFetchWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range work {
				path, err := fetchSticker(ctx, dir, job.md5, job.url)
				if err != nil {
					// Only a definite answer (wrong bytes, 4xx, bad host) is
					// remembered; a timeout or network error retries next read.
					if errors.Is(err, errStickerRejected) {
						markStickerFailed(dir, job.md5)
					}
					continue
				}
				mu.Lock()
				paths[job.md5] = path
				mu.Unlock()
			}
		}()
	}
	for _, job := range jobs {
		work <- job
	}
	close(work)
	wg.Wait()
	for _, row := range rows {
		if rowString(row, "kind_name") != "sticker" || row[stickerImagePathField] != nil {
			continue
		}
		p, _ := row["message_content_parsed"].(map[string]any)
		if path := paths[strings.ToLower(stringMapValue(p, "md5"))]; path != "" {
			row[stickerImagePathField] = path
		}
	}
}

// emoticonTableURLs looks md5s up in WeChat's emoticon.db, which keeps the
// download URL of stickers the account has seen or saved.
func (s *server) emoticonTableURLs(sums []string) map[string]string {
	if !s.ok {
		return nil // only piggyback on a reader that is already set up
	}
	db, err := s.openDBNoRefresh("emoticon", "emoticon.db")
	if err != nil {
		return nil
	}
	defer db.Close()
	marks := strings.TrimSuffix(strings.Repeat("?,", len(sums)), ",")
	args := make([]any, len(sums))
	for i, sum := range sums {
		args[i] = sum
	}
	rows, err := db.Query("SELECT md5, cdn_url FROM kNonStoreEmoticonTable WHERE md5 IN ("+marks+") AND cdn_url != ''", args...)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[strings.ToLower(rowString(row, "md5"))] = rowString(row, "cdn_url")
	}
	return out
}

func cachedStickerPath(dir, sum string) string {
	for _, ext := range []string{"gif", "png", "jpg", "webp"} {
		path := filepath.Join(dir, sum+"."+ext)
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
			return path
		}
	}
	return ""
}

// stickerFailMarker records a definite rejection. Up to 2.0.8 timeouts were
// recorded too, under ".failed"; the new name lets those retry at once.
func stickerFailMarker(dir, sum string) string { return filepath.Join(dir, sum+".rejected") }

func stickerRecentlyFailed(dir, sum string) bool {
	st, err := os.Stat(stickerFailMarker(dir, sum))
	return err == nil && time.Since(st.ModTime()) < stickerNegativeTTL
}

func markStickerFailed(dir, sum string) {
	if strictReadOnlyMode() {
		return
	}
	_, _ = writeMediaCacheFile(stickerFailMarker(dir, sum), []byte("1"))
}

var errStickerRejected = errors.New("sticker download rejected")

func fetchSticker(ctx context.Context, dir, sum, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !stickerHostAllowed(u.Hostname()) {
		return "", fmt.Errorf("%w: host not allowed", errStickerRejected)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := stickerHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: http %d", errStickerRejected, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, stickerMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > stickerMaxBytes {
		return "", fmt.Errorf("%w: size %d", errStickerRejected, len(data))
	}
	got := md5.Sum(data)
	if hex.EncodeToString(got[:]) != sum {
		return "", fmt.Errorf("%w: md5 mismatch", errStickerRejected)
	}
	ext := stickerImageExt(data)
	if ext == "" {
		return "", fmt.Errorf("%w: not an image", errStickerRejected)
	}
	return writeMediaCacheFile(filepath.Join(dir, sum+"."+ext), data)
}

func stickerImageExt(data []byte) string {
	switch {
	case len(data) >= 6 && (string(data[:6]) == "GIF89a" || string(data[:6]) == "GIF87a"):
		return "gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	}
	switch imageExtForData(data) {
	case "png":
		return "png"
	case "jpg":
		return "jpg"
	}
	return ""
}

// agentStickerImages is the agent view: the sticker as an image to look at.
func agentStickerImages(r wcdb.Row) ([]map[string]any, []string) {
	if path := rowString(r, stickerImagePathField); path != "" {
		return []map[string]any{{"path": path, "sticker": true}}, nil
	}
	if !stickerFetchEnabled() {
		return nil, []string{"sticker_fetch_disabled"}
	}
	return nil, []string{"sticker_image_unavailable"}
}
