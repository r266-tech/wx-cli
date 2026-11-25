package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os/exec"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

// wrapWXGF builds the on-disk layout: "wxgf" + header, then each HEVC
// partition preceded by its 4-byte big-endian length.
func wrapWXGF(parts ...[]byte) []byte {
	header := []byte("wxgf")
	header = append(header, 8, 0, 0, 0)
	var b bytes.Buffer
	b.Write(header)
	for _, p := range parts {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(p)))
		b.Write(n[:])
		b.Write(p)
	}
	return b.Bytes()
}

func annexB(size int) []byte {
	p := append([]byte{0, 0, 0, 1}, bytes.Repeat([]byte{0x42}, size-4)...)
	return p
}

func TestWXGFStillPartitionPicksDominantStream(t *testing.T) {
	small, big := annexB(40), annexB(4000)
	got, err := wxgfStillPartition(wrapWXGF(small, big))
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("got %d bytes err=%v, want the %d-byte partition", len(got), err, len(big))
	}
	frames := [][]byte{}
	for i := 0; i < 8; i++ {
		frames = append(frames, annexB(500))
	}
	if _, err := wxgfStillPartition(wrapWXGF(frames...)); !errors.Is(err, errWXGFAnimated) {
		t.Fatalf("many equal partitions must be treated as animated, err=%v", err)
	}
	if _, err := wxgfStillPartition([]byte("not a wxgf payload")); err == nil {
		t.Fatal("invalid header accepted")
	}
}

func TestWXGFWithoutFFmpegReportsActionableStatus(t *testing.T) {
	prev := locateFFmpeg
	locateFFmpeg = func() string { return "" }
	defer func() { locateFFmpeg = prev }()
	_, _, meta, status, err := finishWXGF(wrapWXGF(annexB(4000)), map[string]any{})
	if status != "needs_ffmpeg" || !errors.Is(err, errWXGFNeedsFFmpeg) || meta["source_format"] != "wxgf" {
		t.Fatalf("status=%q err=%v meta=%v", status, err, meta)
	}
}

func TestWXGFConvertsRealHEVCToJPEG(t *testing.T) {
	ffmpeg := findFFmpeg()
	if ffmpeg == "" {
		t.Skip("ffmpeg not installed")
	}
	hevc, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x240", "-frames:v", "1", "-c:v", "libx265", "-f", "hevc", "pipe:1").Output()
	if err != nil || len(hevc) == 0 {
		t.Skip("ffmpeg cannot encode HEVC here")
	}
	jpg, ext, meta, status, err := finishWXGF(wrapWXGF(hevc), map[string]any{})
	if err != nil || status != "decoded" || ext != "jpg" || imageExtForData(jpg) != "jpg" || meta["converter"] != "ffmpeg" {
		t.Fatalf("status=%q ext=%q err=%v", status, ext, err)
	}
}

func TestThumbnailOnlyImageIsFlagged(t *testing.T) {
	row := func(status string, paths ...string) wcdb.Row {
		return wcdb.Row{
			"kind_name": "image",
			"media_read_hints": []any{map[string]any{
				"resource_family":             "image",
				"address_type":                "local_file",
				"direct_readable_local_paths": stringsToAny(paths),
				"local_path_details":          []any{map[string]any{"path": "/x/abc.dat", "decode_status": status}},
			}},
		}
	}
	refs, warnings := agentImageRefs(row("needs_ffmpeg", "/cache/abc_t-0123456789abcdef.jpg"), false)
	if len(refs) != 1 || refs[0]["thumbnail"] != true {
		t.Fatalf("thumbnail ref not flagged: %v", refs)
	}
	want := map[string]bool{"image_thumbnail_only": false, "image_original_needs_ffmpeg": false}
	for _, w := range warnings {
		if _, ok := want[w]; ok {
			want[w] = true
		}
	}
	for w, seen := range want {
		if !seen {
			t.Errorf("missing warning %s in %v", w, warnings)
		}
	}
	refs, warnings = agentImageRefs(row("decoded", "/cache/abc-0123456789abcdef.jpg"), false)
	if len(refs) != 1 || refs[0]["thumbnail"] != nil || len(warnings) != 0 {
		t.Fatalf("original image wrongly flagged: refs=%v warnings=%v", refs, warnings)
	}
}

func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
