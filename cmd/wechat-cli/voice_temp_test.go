package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivatePCMTempIsUniqueAndOwnerOnly(t *testing.T) {
	voice := filepath.Join(t.TempDir(), "fixture.silk")
	first, err := createPrivatePCMTemp(voice)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(first)
	second, err := createPrivatePCMTemp(voice)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(second)
	if first == second {
		t.Fatal("concurrent decodes would share the same PCM path")
	}
	for _, path := range []string{first, second} {
		info, err := os.Stat(path)
		if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			t.Fatalf("PCM temporary file permissions = %v, %v", info, err)
		}
	}
}

func TestWAVPublicationDoesNotFollowDestinationSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "other-file")
	if err := os.WriteFile(target, []byte("leave unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "voice.wav")
	if err := os.Symlink(target, path); err != nil {
		t.Skip(err)
	}
	if err := writePCM16LEMonoWAV(path, []byte{1, 0, 2, 0}, 24000); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "leave unchanged" {
		t.Fatal("WAV writer followed destination symlink")
	}
}

func TestTranscriptPublicationDoesNotFollowDestinationSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "other-file")
	if err := os.WriteFile(target, []byte("leave unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "voice.transcript.json")
	if err := os.Symlink(target, path); err != nil {
		t.Skip(err)
	}
	writeVoiceTranscriptCache(path, map[string]any{"status": "ok", "text": "synthetic transcription"})
	if data, err := os.ReadFile(target); err != nil || string(data) != "leave unchanged" {
		t.Fatal("transcript writer followed destination symlink")
	}
	if got := readVoiceTranscriptCache(path); got["text"] != "synthetic transcription" {
		t.Fatalf("transcript = %#v", got)
	}
}

func TestDecodedMediaDoesNotClobberSharedTemporaryFile(t *testing.T) {
	t.Setenv("WECHAT_CLI_STATE_DIR", t.TempDir())
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	s := &server{}
	dir, err := s.mediaDecodeCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("synthetic media")
	sum := sha256.Sum256(data)
	dst := filepath.Join(dir, "fixture-"+hex.EncodeToString(sum[:8])+".png")
	shared := dst + ".tmp"
	if err := os.WriteFile(shared, []byte("another writer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.writeDecodedMediaCache("fixture.dat", data, "png"); err != nil || got != dst {
		t.Fatalf("decoded output = %s, %v", got, err)
	}
	if b, err := os.ReadFile(shared); err != nil || string(b) != "another writer" {
		t.Fatal("decoded media clobbered another writer's temporary file")
	}
}

func TestVoiceDecodersDoNotClobberOtherPCMAndCleanTemporaryFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test decoder uses a POSIX shell fixture")
	}
	for _, decoder := range []string{"native", "pysilk"} {
		for _, fails := range []bool{false, true} {
			name := decoder + "-success"
			if fails {
				name = decoder + "-failure"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				voice := filepath.Join(root, "fixture.silk")
				legacyPCM := strings.TrimSuffix(voice, ".silk") + ".pcm"
				if err := os.WriteFile(legacyPCM, []byte("other decode"), 0o600); err != nil {
					t.Fatal(err)
				}
				capture := filepath.Join(root, "capture")
				t.Setenv("WECHAT_TEST_PCM_CAPTURE", capture)
				script := "#!/bin/sh\npcm=\"$2\"\nif [ \"$1\" = '-c' ]; then pcm=\"$4\"; fi\nprintf '%s' \"$pcm\" > \"$WECHAT_TEST_PCM_CAPTURE\"\nprintf '\\001\\000\\002\\000' > \"$pcm\"\n"
				if fails {
					script += "exit 7\n"
				}
				fake := filepath.Join(root, "decoder")
				if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("WECHAT_CLI_SILK_DECODER", fake)
				var err error
				if decoder == "native" {
					_, err = (&server{}).decodeSILKVoiceToWAV(voice)
				} else {
					_, err = (&server{}).decodeSILKVoiceToWAVWithPysilk(voice, fake)
				}
				if (err != nil) != fails {
					t.Fatalf("decode error = %v, failure expected = %v", err, fails)
				}
				pcmPath, err := os.ReadFile(capture)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(string(pcmPath)); !os.IsNotExist(err) {
					t.Fatalf("temporary PCM was not removed: %v", err)
				}
				if data, err := os.ReadFile(legacyPCM); err != nil || string(data) != "other decode" {
					t.Fatalf("another decoder's PCM was changed: %q, %v", data, err)
				}
			})
		}
	}
}
