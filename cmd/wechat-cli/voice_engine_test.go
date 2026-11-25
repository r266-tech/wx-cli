package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

func TestWhisperCLIArgsPinChineseAndSimplified(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	args := strings.Join(whisperCLIArgs("/m.bin", "/a.wav"), " ")
	if !strings.Contains(args, "-l zh") || !strings.Contains(args, "--prompt 以下是普通话的句子。") {
		t.Fatalf("args = %s", args)
	}
	t.Setenv("WECHAT_CLI_VOICE_LANGUAGE", "en")
	if args := strings.Join(whisperCLIArgs("/m.bin", "/a.wav"), " "); strings.Contains(args, "--prompt") || !strings.Contains(args, "-l en") {
		t.Fatalf("non-zh args = %s", args)
	}
}

func TestPreferredVoiceEngineFavorsWhisperCPP(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "whisper-cli")
	model := filepath.Join(dir, "ggml-large-v3-turbo-q5_0.bin")
	for _, p := range []string{cli, model} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WECHAT_CLI_WHISPER_CLI", cli)
	t.Setenv("WECHAT_CLI_WHISPER_MODEL", model)
	if got := preferredVoiceEngine(); got != "whisper.cpp" {
		t.Fatalf("engine = %s", got)
	}
	t.Setenv("WECHAT_CLI_VOICE_TRANSCRIBE_CMD", "echo hi")
	if got := preferredVoiceEngine(); got != "custom" {
		t.Fatalf("engine = %s", got)
	}
}

func TestVoiceFailureCachedOnlyForSameEngineAndAnHour(t *testing.T) {
	t.Setenv("WECHAT_CLI_VOICE_TRANSCRIBE_CMD", "echo hi")
	path := filepath.Join(t.TempDir(), "v.transcript.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !recentVoiceTranscriptFailure(path, map[string]any{"status": "unavailable", "engine": "custom"}) {
		t.Fatal("fresh failure by the same engine should be reused")
	}
	if recentVoiceTranscriptFailure(path, map[string]any{"status": "unavailable", "engine": "faster-whisper"}) {
		t.Fatal("a failure from another engine must be retried")
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(path, old, old)
	if recentVoiceTranscriptFailure(path, map[string]any{"status": "unavailable", "engine": "custom"}) {
		t.Fatal("an hour-old failure must be retried")
	}
}

func TestPendingVoiceTranscriptWarns(t *testing.T) {
	r := wcdb.Row{"kind_name": "voice", "media_read_hints": []any{map[string]any{"resource_family": "voice", "transcript": map[string]any{"status": "pending"}}}}
	_, warnings := agentVoiceTranscript(r, false)
	if len(warnings) != 1 || warnings[0] != "voice_transcription_pending" {
		t.Fatalf("warnings = %v", warnings)
	}
}
