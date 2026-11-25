package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// WeChat 4.x stores most original images as WXGF: a small "wxgf" header
// followed by HEVC Annex-B partitions, each preceded by a 4-byte big-endian
// length. A still image has one dominant partition; animated stickers have
// many small ones.

var (
	errWXGFNeedsFFmpeg = errors.New("WXGF original image needs ffmpeg to convert; install ffmpeg (macOS: `brew install ffmpeg`, no user input needed), then read again")
	errWXGFAnimated    = errors.New("animated WXGF image is not converted; the thumbnail is returned")
)

const wxgfStillMinRatio = 0.6

func wxgfStillPartition(data []byte) ([]byte, error) {
	if len(data) < 15 || !bytes.Equal(data[:4], []byte("wxgf")) {
		return nil, fmt.Errorf("invalid WXGF header")
	}
	headerLen := int(data[4])
	if headerLen >= len(data) {
		return nil, fmt.Errorf("invalid WXGF header length")
	}
	start := []byte{0x00, 0x00, 0x00, 0x01}
	var best []byte
	count := 0
	for pos := headerLen; pos < len(data); {
		idx := bytes.Index(data[pos:], start)
		if idx < 0 {
			break
		}
		abs := pos + idx
		if abs < 4 {
			pos = abs + 1
			continue
		}
		length := int(data[abs-4])<<24 | int(data[abs-3])<<16 | int(data[abs-2])<<8 | int(data[abs-1])
		if length <= 0 || abs+length > len(data) {
			pos = abs + 1
			continue
		}
		count++
		if length > len(best) {
			best = data[abs : abs+length]
		}
		pos = abs + length
	}
	if best == nil {
		return nil, fmt.Errorf("no HEVC partition in WXGF data")
	}
	if count > 1 && float64(len(best))/float64(len(data)) < wxgfStillMinRatio {
		return nil, errWXGFAnimated
	}
	return best, nil
}

var (
	ffmpegOnce sync.Once
	ffmpegPath string
	// locateFFmpeg is swapped in tests.
	locateFFmpeg = findFFmpeg
)

// findFFmpeg also checks the Homebrew prefixes because agents often run with
// a minimal PATH that omits them. WECHAT_CLI_FFMPEG=off disables conversion.
func findFFmpeg() string {
	ffmpegOnce.Do(func() {
		if env := strings.TrimSpace(os.Getenv("WECHAT_CLI_FFMPEG")); env != "" {
			if strings.EqualFold(env, "off") {
				return
			}
			if p, err := exec.LookPath(env); err == nil {
				ffmpegPath = p
				return
			}
		}
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpegPath = p
			return
		}
		for _, p := range []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg"} {
			if found, err := exec.LookPath(p); err == nil {
				ffmpegPath = found
				return
			}
		}
	})
	return ffmpegPath
}

func convertWXGFToJPEG(data []byte) ([]byte, error) {
	hevc, err := wxgfStillPartition(data)
	if err != nil {
		return nil, err
	}
	ffmpeg := locateFFmpeg()
	if ffmpeg == "" {
		return nil, errWXGFNeedsFFmpeg
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "hevc", "-i", "pipe:0", "-frames:v", "1", "-q:v", "3", "-f", "image2", "-c:v", "mjpeg", "pipe:1")
	cmd.Stdin = bytes.NewReader(hevc)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg could not convert WXGF: %v %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	out := stdout.Bytes()
	if imageExtForData(out) != "jpg" {
		return nil, fmt.Errorf("ffmpeg produced no JPEG from WXGF")
	}
	return out, nil
}

// finishWXGF turns a decrypted WXGF payload into a viewable JPEG when it can,
// and otherwise reports a status the agent can act on.
func finishWXGF(decoded []byte, meta map[string]any) ([]byte, string, map[string]any, string, error) {
	meta["source_format"] = "wxgf"
	jpg, err := convertWXGFToJPEG(decoded)
	switch {
	case err == nil:
		meta["converter"] = "ffmpeg"
		return jpg, "jpg", meta, "decoded", nil
	case errors.Is(err, errWXGFNeedsFFmpeg):
		return nil, "", meta, "needs_ffmpeg", err
	default:
		return nil, "", meta, "unsupported_format", err
	}
}
