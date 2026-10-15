package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

func unsyncedShardFixture(t *testing.T) (*server, string) {
	t.Helper()
	s, talker := reusedLocalIDFixture(t)
	for _, shard := range []string{"message_0.db", "message_1.db"} {
		db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "message", shard), s.cfg.Keys)
		if err != nil {
			t.Fatal(err)
		}
		sql := "UPDATE " + quoteIdent("Msg_"+talkerHash(talker)) + " SET server_id=0"
		if shard == "message_1.db" {
			sql += ",sort_seq=100,create_time=100"
		}
		err = db.Exec(sql)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return s, talker
}

func TestMessageTokensPageUnsyncedShardTiesAndLocateContext(t *testing.T) {
	s, talker := unsyncedShardFixture(t)
	var firstToken string
	for _, tc := range []struct {
		order, boundary, next string
		want                  []string
	}{
		{"asc", "after_message_token", "next_after_message_token", []string{"mine one", "newer theirs", "theirs two", "mine three", "theirs four"}},
		{"desc", "before_message_token", "next_before_message_token", []string{"theirs four", "mine three", "theirs two", "newer theirs", "mine one"}},
	} {
		args := map[string]any{"talker": talker, "order": tc.order, "limit": 1, "include_media_paths": false}
		seen := map[string]bool{}
		for _, want := range tc.want {
			raw, err := s.toolChatTimeline(args)
			if err != nil {
				t.Fatal(err)
			}
			env := raw.(map[string]any)
			messages := mapSliceAny(env["messages"])
			if len(messages) != 1 || messages[0]["text"] != want {
				t.Fatalf("%s token page want %q: %#v", tc.order, want, messages)
			}
			token := getStr(mapStringAny(messages[0]["id"]), "message_token")
			if !strings.HasPrefix(token, "msg:v1:") || seen[token] {
				t.Fatalf("missing/repeated token: %q", token)
			}
			seen[token] = true
			if firstToken == "" {
				firstToken = token
			}
			if got := getStr(mapStringAny(mapStringAny(env["query"])["cursor"]), tc.next); got != token {
				t.Fatalf("cursor token=%q, want %q", got, token)
			}
			args[tc.boundary] = token
			context, err := s.toolMessageContext(map[string]any{"talker": talker, "message_token": token, "before_count": 1, "after_count": 1, "include_media_paths": false})
			if err != nil {
				t.Fatal(err)
			}
			anchors := 0
			for _, msg := range mapSliceAny(context.(map[string]any)["messages"]) {
				if msg["context_role"] == "anchor" {
					anchors++
					if msg["text"] != want {
						t.Fatalf("wrong token anchor: %#v", msg)
					}
				}
			}
			if anchors != 1 {
				t.Fatalf("anchor roles=%d, want 1", anchors)
			}
		}
		raw, err := s.toolChatTimeline(args)
		if err != nil || len(mapSliceAny(raw.(map[string]any)["messages"])) != 0 {
			t.Fatalf("cursor replayed end of source: %#v, %v", raw, err)
		}
	}
	if _, err := s.toolMessageContext(map[string]any{"talker": talker, "local_id": 1, "include_media_paths": false}); err == nil || !strings.Contains(err.Error(), "message_token") {
		t.Fatalf("ambiguous legacy local ID should require token: %v", err)
	}
	if _, err := s.toolMessageContext(map[string]any{"talker": talker, "message_token": firstToken, "local_id": 2}); err == nil || !strings.Contains(err.Error(), "message_cursor_mismatch") {
		t.Fatalf("inconsistent legacy ID ignored: %v", err)
	}
}

func TestMessageTokensRejectOtherScopeAndChangedSource(t *testing.T) {
	for _, change := range []string{"account", "account_root", "talker", "delete", "migrate", "anchor_keys", "promoted_server", "table_removed"} {
		t.Run(change, func(t *testing.T) {
			s, talker := unsyncedShardFixture(t)
			raw, err := s.toolChatTimeline(map[string]any{"talker": talker, "order": "asc", "limit": 1, "include_media_paths": false})
			if err != nil {
				t.Fatal(err)
			}
			token := getStr(mapStringAny(mapSliceAny(raw.(map[string]any)["messages"])[0]["id"]), "message_token")
			if token == "" {
				t.Fatal("missing message token")
			}
			want := "message_cursor_stale"
			switch change {
			case "account":
				s.cfg.Wxid = "another_account"
				want = "message_cursor_scope_mismatch"
			case "account_root":
				s.cfg.DBRoot = t.TempDir()
				want = "message_cursor_scope_mismatch"
			case "talker":
				talker = "other@chatroom"
				want = "message_cursor_scope_mismatch"
			case "migrate":
				if err := os.Rename(filepath.Join(s.cfg.DBRoot, "db_storage", "message", "message_0.db"), filepath.Join(s.cfg.DBRoot, "db_storage", "message", "message_2.db")); err != nil {
					t.Fatal(err)
				}
			default:
				db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "message", "message_0.db"), s.cfg.Keys)
				if err != nil {
					t.Fatal(err)
				}
				query := "DELETE FROM " + quoteIdent("Msg_"+talkerHash(talker)) + " WHERE local_id=1"
				if change == "anchor_keys" {
					query = "UPDATE " + quoteIdent("Msg_"+talkerHash(talker)) + " SET sort_seq=999 WHERE local_id=1"
				} else if change == "promoted_server" {
					query = "UPDATE " + quoteIdent("Msg_"+talkerHash(talker)) + " SET server_id=9001 WHERE local_id=1"
				} else if change == "table_removed" {
					query = "DROP TABLE " + quoteIdent("Msg_"+talkerHash(talker))
				}
				err = db.Exec(query)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.toolMessageContext(map[string]any{"talker": talker, "message_token": token, "include_media_paths": false}); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s token error=%v, want %s", change, err, want)
			}
			if _, err := s.toolChatTimeline(map[string]any{"talker": talker, "after_message_token": token, "include_media_paths": false}); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s paging error=%v, want %s", change, err, want)
			}
		})
	}
}

func TestMessageTokenValidationAndIncompleteSource(t *testing.T) {
	s, talker := unsyncedShardFixture(t)
	for _, raw := range []string{"msg:v2:e30", "msg:v1:???", "msg:v1:e30", "msg:v1:" + strings.Repeat("A", 4096)} {
		if _, err := s.toolChatTimeline(map[string]any{"talker": talker, "after_message_token": raw, "include_media_paths": false}); err == nil || !strings.Contains(err.Error(), "message_cursor_invalid") {
			t.Fatalf("malformed cursor accepted: %v", err)
		}
	}
	raw, err := s.toolChatTimeline(map[string]any{"talker": talker, "order": "asc", "limit": 1, "include_media_paths": false})
	if err != nil {
		t.Fatal(err)
	}
	token := getStr(mapStringAny(mapSliceAny(raw.(map[string]any)["messages"])[0]["id"]), "message_token")
	parsed, err := parseMessageToken(token)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range []string{"../message_0.db", "/tmp/message_0.db", "contact.db", `..\message_0.db`} {
		parsed.Shard = shard
		data, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseMessageToken(messageTokenPrefix + base64.RawURLEncoding.EncodeToString(data)); err == nil {
			t.Fatalf("path-like token shard accepted: %q", shard)
		}
	}
	if err := os.WriteFile(filepath.Join(s.cfg.DBRoot, "db_storage", "message", "message_9.db"), []byte("fixture unreadable shard"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolChatTimeline(map[string]any{"talker": talker, "after_message_token": token, "include_media_paths": false}); err == nil || !strings.Contains(err.Error(), "message_cursor_source_incomplete") {
		t.Fatalf("token advanced despite unreadable source: %v", err)
	}
	if _, err := s.toolReadEvents(map[string]any{"talker": talker, "include_media_paths": false}); err == nil {
		t.Fatal("initial message tail returned an advancing cursor from incomplete source")
	}
}

func TestHistoryAndSearchExposeUsableMessageTokens(t *testing.T) {
	s, talker := reusedLocalIDFixture(t)
	raw, err := s.toolMessages(map[string]any{"talker": talker, "view": "agent", "include_media_paths": false})
	if err != nil {
		t.Fatal(err)
	}
	messages := mapSliceAny(raw.(map[string]any)["messages"])
	for _, message := range messages {
		token := getStr(mapStringAny(message["id"]), "message_token")
		if _, err := parseMessageToken(token); err != nil {
			t.Fatalf("history message token unavailable: %v", err)
		}
	}
	raw, err = s.toolSearchWithContext(map[string]any{"talker": talker, "keyword": "hit", "limit": 2, "context_limit": 2, "include_media_paths": false})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range mapSliceAny(raw.(map[string]any)["hits"]) {
		msg := mapStringAny(hit["message"])
		// The CLI adapter owns id fields; the context itself must use the token.
		ctx := mapStringAny(hit["context"])
		if len(ctx) == 0 {
			t.Fatalf("search token context failed: %#v", hit)
		}
		anchor := mapStringAny(mapStringAny(ctx["query"])["anchor"])
		if getStr(anchor, "message_token") == "" || anchor["server_id_str"] != mapStringAny(msg["id"])["server_id_str"] {
			t.Fatalf("search expanded wrong token anchor: %#v", hit)
		}
	}
}
