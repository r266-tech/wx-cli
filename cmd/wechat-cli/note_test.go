package main

import (
	"strings"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/wxkind"
)

// A WeChat note (appmsg type 24) used to be reported as an unreadable file
// labelled "[应用消息]".
func TestWeChatNoteIsNotAFile(t *testing.T) {
	if got := wxkind.Resolve(49, 24); got != "note" {
		t.Fatalf("appmsg 24 = %q, want note", got)
	}
	parsed := map[string]any{"title": "", "des": "周末计划\n第二行"}
	if got := contentSummary(49, 24, "", parsed); got != "[笔记] 周末计划" {
		t.Fatalf("summary = %q", got)
	}
	if got := contentSummary(49, 24, "", map[string]any{}); got != "[笔记]" {
		t.Fatalf("empty note summary = %q", got)
	}
	_, fileArgs, err := messageKindWhere("file", "local_type")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range fileArgs {
		if a == packedLocalType(49, 24) {
			t.Fatal("--type file still matches notes")
		}
	}
	clause, noteArgs, err := messageKindWhere("note", "local_type")
	if err != nil || !strings.Contains(clause, "=") || len(noteArgs) != 1 || noteArgs[0] != packedLocalType(49, 24) {
		t.Fatalf("--type note clause=%q args=%v err=%v", clause, noteArgs, err)
	}
}

func TestFileRefsDropWeChatDuplicateDownloads(t *testing.T) {
	refs := []map[string]any{
		{"name": "plan.dmg", "file_size": int64(10), "path": "/f/plan.dmg"},
		{"name": "plan(2).dmg", "file_size": int64(10), "path": "/f/plan(2).dmg"},
		{"name": "plan(3).dmg", "file_size": int64(99), "path": "/f/plan(3).dmg"},
	}
	got := dedupeAgentFileRefs(refs)
	if len(got) != 2 || got[0]["path"] != "/f/plan.dmg" || got[1]["path"] != "/f/plan(3).dmg" {
		t.Fatalf("got %v", got)
	}
}
