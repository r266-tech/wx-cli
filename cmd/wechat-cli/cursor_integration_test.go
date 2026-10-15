package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestTailMessageTokensResumeUnsyncedShardTies(t *testing.T) {
	s, talker := unsyncedShardFixture(t)
	raw, err := s.toolChatTimeline(map[string]any{"talker": talker, "order": "asc", "limit": 1, "include_media_paths": false})
	if err != nil {
		t.Fatal(err)
	}
	first := mapSliceAny(raw.(map[string]any)["messages"])[0]
	cursor := getStr(mapStringAny(first["id"]), "message_token")
	args := map[string]any{"talker": talker, "cursor": cursor, "limit": 1, "include_media_paths": false, "since_local_id": 999, "since_time": "9999"}
	for _, want := range []string{"newer theirs", "theirs two", "mine three", "theirs four"} {
		result, err := s.toolReadEvents(args)
		if err != nil {
			t.Fatal(err)
		}
		env := result.(map[string]any)
		events := mapSliceAny(env["events"])
		if len(events) != 1 || mapStringAny(events[0]["message"])["text"] != want {
			t.Fatalf("tail wanted %q: %#v", want, events)
		}
		next := getStr(env, "cursor")
		if !strings.HasPrefix(next, messageTokenPrefix) || next == getStr(args, "cursor") || next != getStr(events[0], "cursor") {
			t.Fatalf("invalid tail cursor progression: %#v", env)
		}
		args["cursor"] = next
	}
	last, err := s.toolReadEvents(args)
	if err != nil {
		t.Fatal(err)
	}
	if env := last.(map[string]any); len(mapSliceAny(env["events"])) != 0 || env["cursor"] != args["cursor"] {
		t.Fatalf("empty tail changed checkpoint: %#v", env)
	}
}

func TestDigestMessageTokensResumeUnsyncedShardTies(t *testing.T) {
	s, talker := unsyncedShardFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	args := map[string]any{"talker": talker, "since_last": true, "limit": 1, "include_media_paths": false}
	for _, want := range []string{"mine one", "newer theirs", "theirs two", "mine three", "theirs four"} {
		raw, err := s.toolDigestSource(args)
		if err != nil {
			t.Fatal(err)
		}
		env := raw.(map[string]any)
		data, err := os.ReadFile(getStr(env, "source_json"))
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		messages := mapSliceAny(payload["messages"])
		if len(messages) != 1 || messages[0]["text"] != want {
			t.Fatalf("digest wanted %q: %#v", want, messages)
		}
		data, err = os.ReadFile(getStr(env, "checkpoint"))
		if err != nil {
			t.Fatal(err)
		}
		var state digestCursor
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		if state.MessageToken != getStr(mapStringAny(messages[0]["id"]), "message_token") {
			t.Fatalf("digest saved wrong shard checkpoint: %#v", state)
		}
	}
	raw, err := s.toolDigestSource(args)
	if err != nil {
		t.Fatal(err)
	}
	if raw.(map[string]any)["message_count"] != 0 {
		t.Fatalf("digest replayed messages: %#v", raw)
	}
}

func TestReadEventsJSONLRetainsEmptyCheckpointAndDiagnostics(t *testing.T) {
	env := map[string]any{"events": []map[string]any{}, "cursor": "session-inline:v1:fixture:0", "query": map[string]any{"has_more": true}, "freshness": map[string]any{"cursor_scope": "batch"}, "warnings": []string{"session_scan_truncated"}}
	var buf bytes.Buffer
	if err := writeReadEventsJSONL(json.NewEncoder(&buf), env); err != nil {
		t.Fatal(err)
	}
	var checkpoint map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint["type"] != "checkpoint" || checkpoint["cursor"] != env["cursor"] || !getBoolDefault(mapStringAny(checkpoint["query"]), "has_more", false) || len(stringSliceAny(checkpoint["warnings"])) != 1 {
		t.Fatalf("empty stream dropped state or diagnostics: %#v", checkpoint)
	}
	if readEventsBatchCanDrain(env, getStr(env, "cursor")) {
		t.Fatal("blocked scan would busy-loop")
	}
	if !readEventsBatchCanDrain(env, "previous") {
		t.Fatal("progressing backlog should drain immediately")
	}
	if readEventsBatchCanDrain(map[string]any{"cursor": "new", "query": map[string]any{"has_more": false}}, "old") {
		t.Fatal("caught-up follow should wait")
	}
}

func TestSearchCLIExposesMessageToken(t *testing.T) {
	row := cliSearchMessageRow(map[string]any{"local_id": int64(1), "message_token": "msg:v1:fixture"}, nil)
	if getStr(mapStringAny(row["id"]), "message_token") != "msg:v1:fixture" {
		t.Fatal("search dropped stable locator")
	}
}

func TestCursorRecoveryAdvicePreservesUnreadableCheckpoint(t *testing.T) {
	advice := cliErrorAdvice("tool_error", "after_message anchor: message_cursor_source_incomplete: shard unavailable", "read_events", "tail")
	if !strings.Contains(advice.NextAction, "Keep the existing cursor") {
		t.Fatalf("incomplete source should preserve checkpoint: %#v", advice)
	}
	advice = cliErrorAdvice("tool_error", "message_cursor_stale: original message shard is no longer available", "read_events", "tail")
	if !strings.Contains(advice.NextAction, "fresh timeline") {
		t.Fatalf("stale token recovery not actionable: %#v", advice)
	}
}
