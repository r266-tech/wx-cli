package main

import (
	"strings"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
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

func TestPrivateChatQuoteSenderUsesChatName(t *testing.T) {
	r := wcdb.Row{
		"kind_name":           "quote",
		"talker":              "wxid_friend",
		"talker_display_name": "Friend",
		"sender_wxid":         "wxid_me",
		"message_content_parsed": map[string]any{"refermsg": map[string]any{
			"type": int64(1), "fromusr": "wxid_friend", "content_raw": "hi", "svrid": "1",
		}},
	}
	if q := agentQuote(r, nil); q["sender"] != "Friend" {
		t.Fatalf("quote sender = %v, want the chat display name", q["sender"])
	}
}

func TestFileRefsKeepOnlyThisMessagesFile(t *testing.T) {
	refs := []map[string]any{
		{"name": "index.html", "file_size": int64(11906), "path": "/x/index.html"},
		{"name": "index(2).html", "file_size": int64(68385), "path": "/x/index(2).html"},
	}
	got := keepDeclaredSizeFileRefs(refs, map[string]any{"name": "index.html", "file_size": int64(68385)})
	if len(got) != 1 || got[0]["path"] != "/x/index(2).html" {
		t.Fatalf("want only the size-matching file, got %#v", got)
	}
	same := []map[string]any{
		{"name": "a(3)(1).html", "file_size": int64(5)},
		{"name": "a(3).html", "file_size": int64(5)},
	}
	got = dedupeAgentFileRefs(keepDeclaredSizeFileRefs(same, map[string]any{"name": "a(3).html", "file_size": int64(5)}))
	if len(got) != 1 || got[0]["name"] != "a(3).html" {
		t.Fatalf("want the exact-name copy only, got %#v", got)
	}
	none := keepDeclaredSizeFileRefs(refs, map[string]any{"file_size": int64(1)})
	if len(none) != 2 {
		t.Fatalf("no size match must keep all refs, got %#v", none)
	}
}

func TestDeclaredFileFallsBackToResourceSize(t *testing.T) {
	r := wcdb.Row{
		"kind_name":              "file",
		"message_content_parsed": map[string]any{"title": "index.html"},
		"media_resources":        []map[string]any{{"resource_family": "file", "file_name": "index.html", "size": int64(68385)}},
	}
	got := agentDeclaredFile(r)
	if n, _ := integerArgValue(got["file_size"]); n != 68385 {
		t.Fatalf("declared size = %#v", got)
	}
}

func TestMediaRowFileRefsPickThisMessagesCopy(t *testing.T) {
	r := wcdb.Row{
		"kind_name": "file",
		"resources": []map[string]any{{"resource_family": "file", "file_name": "index.html", "size": int64(68385)}},
	}
	refs := []map[string]any{
		{"name": "index.html", "file_size": int64(11906)},
		{"name": "index.html", "file_size": int64(68385)},
	}
	got := keepDeclaredSizeFileRefs(refs, agentDeclaredFile(r))
	if len(got) != 1 {
		t.Fatalf("got %#v", got)
	}
	if n, _ := integerArgValue(got[0]["file_size"]); n != 68385 {
		t.Fatalf("kept the wrong copy: %#v", got)
	}
}
