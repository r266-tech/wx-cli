package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

func sessionTailFixtureCall(t *testing.T, s *server, args map[string]any) map[string]any {
	t.Helper()
	raw, err := s.readSessionEvents(args)
	if err != nil {
		t.Fatal(err)
	}
	return raw.(map[string]any)
}

func mutateSessionFixture(t *testing.T, s *server, sql string) {
	t.Helper()
	db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "session", "session.db"), s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Exec(sql)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionStateDiffTracksSameSecondChangesAdditionAndRemoval(t *testing.T) {
	s, _ := liveSessionFixture(t)
	first := sessionTailFixtureCall(t, s, nil)
	mutateSessionFixture(t, s, `UPDATE SessionTable SET summary='same second correction', unread_count=0 WHERE username='fixture_group@chatroom';
		DELETE FROM SessionTable WHERE username='fixture_friend';
		INSERT INTO SessionTable VALUES ('new_friend',1,'new',500,500,'','',1,0,0);`)
	next := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(first, "cursor")})
	events := mapSliceAny(next["events"])
	if len(events) != 3 {
		t.Fatalf("changed session events = %#v", events)
	}
	byName := map[string]map[string]any{}
	for _, event := range events {
		byName[getStr(mapStringAny(event["session"]), "username")] = event
	}
	for name, want := range map[string]string{"fixture_group@chatroom": "updated", "fixture_friend": "removed", "new_friend": "added"} {
		if byName[name]["change"] != want || byName[name]["session_id"] != sessionIdentity(name) {
			t.Fatalf("event for %s = %#v, want %s", name, byName[name], want)
		}
	}
	quiet := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(next, "cursor")})
	if len(mapSliceAny(quiet["events"])) != 0 || quiet["cursor"] != next["cursor"] {
		t.Fatal("unchanged poll emitted a change or rewrote its cursor")
	}
}

func TestSessionInitialSnapshotAcknowledgesOlderRows(t *testing.T) {
	s, _ := liveSessionFixture(t)
	first := sessionTailFixtureCall(t, s, map[string]any{"limit": 1})
	events := mapSliceAny(first["events"])
	if len(events) != 1 || sessionEventUsername(events[0]) != "fixture_group@chatroom" || !getBool(mapStringAny(first["query"]), "initial_snapshot") {
		t.Fatalf("initial snapshot = %#v", first)
	}
	quiet := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(first, "cursor"), "limit": 10})
	if len(mapSliceAny(quiet["events"])) != 0 {
		t.Fatal("older snapshot rows became an unwanted initial backlog")
	}
	mutateSessionFixture(t, s, "UPDATE SessionTable SET unread_count=4 WHERE username='fixture_official'")
	changed := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(quiet, "cursor")})
	if events := mapSliceAny(changed["events"]); len(events) != 1 || events[0]["change"] != "updated" || sessionEventUsername(events[0]) != "fixture_official" {
		t.Fatalf("older session state change was not observed: %#v", events)
	}
}

func TestSessionLocalEventCursorResumesUnconsumedPrefix(t *testing.T) {
	s, root := liveSessionFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	first := sessionTailFixtureCall(t, s, nil)
	if !strings.HasPrefix(getStr(first, "cursor"), sessionLocalCursorPrefix) {
		t.Fatal("normal mode did not use a local cursor")
	}
	mutateSessionFixture(t, s, "UPDATE SessionTable SET summary=summary || ' updated' WHERE is_hidden=0")
	next := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(first, "cursor"), "limit": 2})
	events := mapSliceAny(next["events"])
	if len(events) != 2 || !getBool(mapStringAny(next["query"]), "has_more") {
		t.Fatalf("paged events = %#v", next)
	}
	consumed := sessionEventUsername(events[0])
	resume := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(events[0], "cursor"), "limit": 10})
	remaining := mapSliceAny(resume["events"])
	if len(remaining) != 2 {
		t.Fatalf("remaining changes = %#v", remaining)
	}
	for _, event := range remaining {
		if sessionEventUsername(event) == consumed {
			t.Fatal("already consumed event repeated")
		}
	}
	before := fixtureFileState(t, root)
	quiet := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(resume, "cursor")})
	if quiet["cursor"] != resume["cursor"] || !reflect.DeepEqual(before, fixtureFileState(t, root)) {
		t.Fatal("quiet local poll wrote a new snapshot")
	}
	entries, err := os.ReadDir(filepath.Join(root, "state", "session-cursors"))
	if err != nil || len(entries) != 3 {
		t.Fatalf("one snapshot per changed batch: count=%d err=%v", len(entries), err)
	}
	for _, entry := range entries {
		path := filepath.Join(root, "state", "session-cursors", entry.Name())
		info, err := os.Stat(path)
		if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			t.Fatalf("snapshot permissions: %v, %v", info, err)
		}
		compressed, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := decodeSessionSnapshot(compressed)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(snapshot)
		if bytes.Contains(b, []byte("fresh group")) || bytes.Contains(b, []byte("updated")) || bytes.Contains(b, []byte("Fixture Group")) {
			t.Fatal("cursor snapshot retained message or display text")
		}
	}
}

func TestSessionStrictInlineCursorDoesNotWriteAndUsesBatchCheckpoint(t *testing.T) {
	s, root := liveSessionFixture(t)
	before := fixtureFileState(t, root)
	first := sessionTailFixtureCall(t, s, nil)
	if !strings.HasPrefix(getStr(first, "cursor"), sessionInlineCursorPrefix) || getStr(mapStringAny(first["freshness"]), "cursor_scope") != "batch" {
		t.Fatal("strict mode did not use inline batch cursor")
	}
	for i, event := range mapSliceAny(first["events"]) {
		if _, exists := event["cursor"]; exists || event["cursor_position"] != i+1 {
			t.Fatal("inline event repeated the full snapshot or omitted its position")
		}
	}
	if !reflect.DeepEqual(before, fixtureFileState(t, root)) {
		t.Fatal("strict inline cursor wrote state")
	}
	if _, err := s.readSessionEvents(map[string]any{"cursor_storage": "local"}); err == nil {
		t.Fatal("strict mode accepted local snapshot writes")
	}
}

func TestSessionCursorRejectsAccountAndQueryScopeMixing(t *testing.T) {
	s, _ := liveSessionFixture(t)
	first := sessionTailFixtureCall(t, s, nil)
	for _, args := range []map[string]any{
		{"cursor": getStr(first, "cursor"), "unread_only": true},
		{"cursor": getStr(first, "cursor"), "type_filter": "group"},
		{"cursor": getStr(first, "cursor"), "keyword": "Fixture"},
	} {
		if _, err := s.readSessionEvents(args); err == nil || !strings.Contains(err.Error(), "scope") {
			t.Fatalf("mixed query scope accepted: %v", err)
		}
	}
	cfg := *s.cfg
	cfg.Wxid = "another_fixture_account"
	other := &server{cfg: &cfg, wcdbPath: s.wcdbPath, ok: true}
	if _, err := other.readSessionEvents(map[string]any{"cursor": getStr(first, "cursor")}); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("mixed account accepted: %v", err)
	}
}

func TestSessionCursorBlocksTruncatedScansAndMetadataFailure(t *testing.T) {
	s, _ := liveSessionFixture(t)
	first := sessionTailFixtureCall(t, s, nil)
	truncated := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(first, "cursor"), "limit": 1, "scan_limit": 1})
	if truncated["cursor"] != first["cursor"] || len(mapSliceAny(truncated["events"])) != 0 || !getBool(mapStringAny(truncated["query"]), "has_more") {
		t.Fatal("truncated scan advanced the baseline")
	}
	initial := sessionTailFixtureCall(t, s, map[string]any{"limit": 1, "scan_limit": 1})
	if getStr(initial, "cursor") != "" || len(mapSliceAny(initial["events"])) != 0 {
		t.Fatal("incomplete initial scan established a baseline")
	}
	path := filepath.Join(s.cfg.DBRoot, "db_storage", "contact", "contact.db")
	if err := os.Rename(path, path+".offline"); err != nil {
		t.Fatal(err)
	}
	failed := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(first, "cursor")})
	if failed["cursor"] != first["cursor"] || len(mapSliceAny(failed["events"])) != 0 || !strings.Contains(strings.Join(stringSliceAny(failed["warnings"]), " "), "session_cursor_not_advanced") {
		t.Fatal("metadata failure changed the baseline")
	}
}

func TestSessionKeywordMetadataFailureDoesNotProduceRemoval(t *testing.T) {
	s, _ := liveSessionFixture(t)
	first := sessionTailFixtureCall(t, s, map[string]any{"keyword": "Fixture Group"})
	if len(mapSliceAny(first["events"])) != 1 {
		t.Fatal("fixture keyword did not match")
	}
	path := filepath.Join(s.cfg.DBRoot, "db_storage", "contact", "contact.db")
	if err := os.Rename(path, path+".offline"); err != nil {
		t.Fatal(err)
	}
	next := sessionTailFixtureCall(t, s, map[string]any{"keyword": "Fixture Group", "cursor": getStr(first, "cursor")})
	if next["cursor"] != first["cursor"] || len(mapSliceAny(next["events"])) != 0 || !strings.Contains(strings.Join(stringSliceAny(next["warnings"]), " "), "session_contact_lookup_unavailable") {
		t.Fatal("failed keyword enrichment was mistaken for removal")
	}
}

func TestSessionUnreadFilterReportsExitFromMatchedSet(t *testing.T) {
	s, _ := liveSessionFixture(t)
	first := sessionTailFixtureCall(t, s, map[string]any{"unread_only": true})
	mutateSessionFixture(t, s, "UPDATE SessionTable SET unread_count=0 WHERE username='fixture_group@chatroom'")
	next := sessionTailFixtureCall(t, s, map[string]any{"unread_only": true, "cursor": getStr(first, "cursor")})
	events := mapSliceAny(next["events"])
	if len(events) != 1 || events[0]["change"] != "removed" || sessionEventUsername(events[0]) != "fixture_group@chatroom" {
		t.Fatalf("filtered removal = %#v", events)
	}
}

func TestSessionLegacyCursorUpgradesWithoutSkippingCatchUp(t *testing.T) {
	s, _ := liveSessionFixture(t)
	first := sessionTailFixtureCall(t, s, map[string]any{"cursor": "session:299", "limit": 1})
	if events := mapSliceAny(first["events"]); len(events) != 1 || sessionEventUsername(events[0]) != "fixture_official" {
		t.Fatalf("legacy first window = %#v", events)
	}
	if !strings.Contains(strings.Join(stringSliceAny(first["warnings"]), " "), "unread_changes_before_upgrade_unavailable") {
		t.Fatal("legacy upgrade omitted its replay limitation")
	}
	next := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(first, "cursor"), "limit": 10})
	if len(mapSliceAny(next["events"])) != 2 {
		t.Fatal("legacy upgrade skipped pending sessions")
	}
	mutateSessionFixture(t, s, "UPDATE SessionTable SET unread_count=0 WHERE username='fixture_official'")
	changed := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(next, "cursor")})
	if events := mapSliceAny(changed["events"]); len(events) != 1 || events[0]["change"] != "updated" {
		t.Fatal("upgraded cursor did not track unread state")
	}
}

func TestSessionCursorValidationRejectsUnsafeOrOversizedPayloads(t *testing.T) {
	username := "fixture"
	hash, _ := sessionFingerprint(map[string]any{"username": username, "unread_count": 1})
	snapshot := sessionCursorSnapshot{Schema: 1, Binding: strings.Repeat("a", 64), Base: map[string]sessionCursorState{sessionIdentity(username): {Username: username, Hash: hash}}, Updates: []sessionCursorUpdate{}}
	compressed, err := encodeSessionSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	valid := sessionInlineCursorPrefix + base64.RawURLEncoding.EncodeToString(compressed) + ":0"
	if _, err := readSessionStateCursor(valid, snapshot.Binding); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{
		strings.TrimSuffix(valid, ":0") + ":1",
		strings.TrimSuffix(valid, ":0") + ":-1",
		strings.TrimSuffix(valid, ":0") + ":00",
		sessionLocalCursorPrefix + "../../private:0",
		sessionInlineCursorPrefix + base64.RawURLEncoding.EncodeToString(append(append([]byte(nil), compressed...), compressed...)) + ":0",
	} {
		if _, err := readSessionStateCursor(token, snapshot.Binding); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	var bomb bytes.Buffer
	w := gzip.NewWriter(&bomb)
	_, _ = w.Write(bytes.Repeat([]byte(" "), sessionCursorMaxJSONBytes+1))
	_ = w.Close()
	if _, err := decodeSessionSnapshot(bomb.Bytes()); err == nil {
		t.Fatal("oversized decompression accepted")
	}
	snapshot.Updates = []sessionCursorUpdate{{ID: sessionIdentity(username), Username: username, Hash: hash}, {ID: sessionIdentity(username), Username: username, Hash: hash}}
	if _, err := encodeSessionSnapshot(snapshot); err == nil {
		t.Fatal("duplicate update identity accepted")
	}
}

func TestSessionLocalCursorMissingSnapshotDoesNotReset(t *testing.T) {
	s, _ := liveSessionFixture(t)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	first := sessionTailFixtureCall(t, s, nil)
	token := getStr(first, "cursor")
	id := strings.Split(strings.TrimPrefix(token, sessionLocalCursorPrefix), ":")[0]
	path, err := sessionSnapshotPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readSessionEvents(map[string]any{"cursor": token}); err == nil {
		t.Fatal("missing cursor silently reset its baseline")
	}
}

func TestSessionEmptyBaselineObservesFirstAddedSession(t *testing.T) {
	s, _ := liveSessionFixture(t)
	mutateSessionFixture(t, s, "DELETE FROM SessionTable")
	first := sessionTailFixtureCall(t, s, nil)
	if getStr(first, "cursor") == "" || len(mapSliceAny(first["events"])) != 0 {
		t.Fatal("empty session set did not produce a resumable baseline")
	}
	mutateSessionFixture(t, s, "INSERT INTO SessionTable VALUES ('first_fixture',1,'first',0,0,'','',1,0,0)")
	next := sessionTailFixtureCall(t, s, map[string]any{"cursor": getStr(first, "cursor")})
	events := mapSliceAny(next["events"])
	if len(events) != 1 || events[0]["change"] != "added" || sessionEventUsername(events[0]) != "first_fixture" {
		t.Fatalf("first session was not detected: %#v", events)
	}
}

func TestSessionLargeInlineCursorRoundTripsJSONStdin(t *testing.T) {
	snapshot := sessionCursorSnapshot{Schema: 1, Binding: strings.Repeat("b", 64), Base: map[string]sessionCursorState{}, Updates: []sessionCursorUpdate{}}
	for i := 0; i < 5000; i++ {
		username := fmt.Sprintf("fixture-user-%d", i)
		hash, err := sessionFingerprint(map[string]any{"username": username, "unread_count": i})
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Base[sessionIdentity(username)] = sessionCursorState{Username: username, Hash: hash}
	}
	prefix, err := writeSessionSnapshot(snapshot, "inline")
	if err != nil {
		t.Fatal(err)
	}
	token := sessionCursorAt(prefix, 0)
	if len(token) < 128<<10 {
		t.Fatalf("fixture is not larger than common argv limits: %d", len(token))
	}
	b, err := json.Marshal(map[string]any{"mode": "sessions", "cursor": token})
	if err != nil {
		t.Fatal(err)
	}
	stdin, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { _, err := writer.Write(b); _ = writer.Close(); done <- err }()
	input, err := io.ReadAll(stdin)
	_ = stdin.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	args, err := decodeCLIJSONArgs(string(input))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := readSessionStateCursor(getStr(args, "cursor"), snapshot.Binding)
	if err != nil || !reflect.DeepEqual(decoded.states(), snapshot.Base) {
		t.Fatalf("large stdin cursor was altered: %v", err)
	}
}
