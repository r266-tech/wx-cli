package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/config"
	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
	"github.com/r266-tech/wx-cli/v2/internal/wxkind"
)

func addReadFixtureDB(t *testing.T, s *server, file, schema string) {
	t.Helper()
	path := filepath.Join(s.cfg.DBRoot, "db_storage", file)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	var salt [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(salt[:]); err != nil {
		t.Fatal(err)
	}
	keyHex, saltHex := hex.EncodeToString(key[:]), hex.EncodeToString(salt[:])
	db, err := wcdb.OpenWithEncKeyWritable(path, keyHex, saltHex)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	s.cfg.Keys[saltHex] = keyHex
}

func TestSessionTailTracksUnreadOnlyChanges(t *testing.T) {
	s, _ := liveSessionFixture(t)
	raw, err := s.toolReadEvents(map[string]any{"mode": "sessions"})
	if err != nil {
		t.Fatal(err)
	}
	cursor := getStr(raw.(map[string]any), "cursor")
	db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "session", "session.db"), s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE SessionTable SET unread_count=0 WHERE username='fixture_group@chatroom'"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	raw, err = s.toolReadEvents(map[string]any{"mode": "sessions", "cursor": cursor})
	if err != nil {
		t.Fatal(err)
	}
	env := raw.(map[string]any)
	freshness := mapStringAny(env["freshness"])
	if freshness["unread_updates_tracked"] != true || freshness["observed_changes"] != "session_state_diff" {
		t.Fatalf("session state diff metadata = %#v", freshness)
	}
	events := mapSliceAny(env["events"])
	if len(events) != 1 || events[0]["change"] != "updated" || int64MapValue(mapStringAny(events[0]["session"]), "unread_count") != 0 {
		t.Fatalf("unread clear was not delivered: %#v", events)
	}
	current, err := s.toolSessions(map[string]any{})
	if err != nil || rowInt64(current.(cliRowsResult).Rows[0], "unread_count") != 0 {
		t.Fatalf("current session snapshot did not expose the read update: %#v, %v", current, err)
	}
}

func TestVoiceLookupRejectsReusedLocalIDsAndConflictingServerIDs(t *testing.T) {
	s, talker := readMessageFixture(t)
	addReadFixtureDB(t, s, "message/media_0.db", fmt.Sprintf(`
		CREATE TABLE Name2Id (user_name TEXT);
		INSERT INTO Name2Id VALUES ('%s');
		CREATE TABLE VoiceInfo (chat_name_id INTEGER, create_time INTEGER, local_id INTEGER, svr_id INTEGER, voice_data BLOB, data_index TEXT);
		INSERT INTO VoiceInfo VALUES (1,100,1,101,X'010203','old');`, talker))
	for _, tc := range []struct {
		name                     string
		local, server, timestamp int64
	}{
		{"reused_local_id", 1, 201, 500},
		{"same_time_conflicting_server", 9, 201, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blob, err := s.voiceBlob(talker, tc.local, tc.server, tc.timestamp)
			if err != nil {
				t.Fatal(err)
			}
			if blob != nil {
				t.Fatalf("different message voice was attached: local=%d server=%d time=%d", blob.localID, blob.serverID, blob.createTime)
			}
		})
	}
	blob, err := s.voiceBlob(talker, 9, 101, 500)
	if err != nil || blob == nil || blob.serverID != 101 {
		t.Fatalf("exact server ID lookup failed: %#v, %v", blob, err)
	}
}

func TestVoiceLookupRequiresUnambiguousMetadataFallback(t *testing.T) {
	s, talker := readMessageFixture(t)
	addReadFixtureDB(t, s, "message/media_0.db", fmt.Sprintf(`
		CREATE TABLE Name2Id (user_name TEXT);
		INSERT INTO Name2Id VALUES ('%s');
		CREATE TABLE VoiceInfo (chat_name_id INTEGER, create_time INTEGER, local_id INTEGER, svr_id INTEGER, voice_data BLOB, data_index TEXT);
		INSERT INTO VoiceInfo VALUES (1,100,1,0,X'010203','fallback');`, talker))
	blob, err := s.voiceBlob(talker, 1, 101, 102)
	if err != nil || blob == nil || blob.dataIndex != "fallback" {
		t.Fatalf("unique metadata fallback failed: %#v, %v", blob, err)
	}
	db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "message", "media_0.db"), s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO VoiceInfo VALUES (1,100,2,0,X'040506','same-second')"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if blob, err := s.voiceBlob(talker, 1, 101, 100); err == nil || blob != nil {
		t.Fatalf("ambiguous voice timestamp chose arbitrary audio: %#v, %v", blob, err)
	}
}

func TestMediaResourceFailurePreservesIndependentXMLHints(t *testing.T) {
	s, talker := readMessageFixture(t)
	rows := enrichMessages([]wcdb.Row{{"talker": talker, "local_id": int64(1), "local_type": int64(3), "message_content": `<msg><img md5="fixture-md5" cdnbigimgurl="fixture-cdn"/></msg>`}})
	if err := s.enrichMessageMediaResources(rows); err == nil {
		t.Fatal("fixture should have no message_resource.db")
	}
	hints := mapSliceAny(rows[0]["media_read_hints"])
	if len(hints) != 1 || hints[0]["source"] != "message_xml" {
		t.Fatalf("unavailable resource DB suppressed independent XML enrichment: %#v", rows[0])
	}
}

func TestSourceLookupUsesConversationScopedServerIdentity(t *testing.T) {
	correct := wcdb.Row{"talker": "fixture_group@chatroom", "local_id": int64(1), "server_id_str": "101", "content_summary": "correct"}
	other := wcdb.Row{"talker": "other@chatroom", "local_id": int64(2), "server_id_str": "101", "content_summary": "other"}
	index := agentSourceMessageIndex([]wcdb.Row{correct, other})
	if got := lookupAgentSourceMessage(index, "fixture_group@chatroom", 0, "101", 0); rowString(got, "content_summary") != "correct" {
		t.Fatalf("global lookup shadowed exact conversation identity: %#v", got)
	}
}

func TestGroupVoiceQuoteUsesConversationForVoiceLookup(t *testing.T) {
	s, talker := readMessageFixture(t)
	addReadFixtureDB(t, s, "message/media_0.db", fmt.Sprintf(`
		CREATE TABLE Name2Id (user_name TEXT);
		INSERT INTO Name2Id VALUES ('%s');
		CREATE TABLE VoiceInfo (chat_name_id INTEGER, create_time INTEGER, local_id INTEGER, svr_id INTEGER, voice_data BLOB, data_index TEXT);
		INSERT INTO VoiceInfo VALUES (1,100,1,101,X'010203','quote-source');`, talker))
	parsed := map[string]any{"duration_ms": int64(1000), "voice_format": int64(4)}
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	path, err := s.writeVoiceCache(talker, &voiceBlob{createTime: 100, localID: 1, serverID: 101, data: []byte{1, 2, 3}}, parsed)
	if err != nil {
		t.Fatal(err)
	}
	writeVoiceTranscriptCache(strings.TrimSuffix(path, filepath.Ext(path))+".transcript.json", map[string]any{"cache_version": voiceTranscriptCacheVersion, "status": "ok", "text": "fixture voice"})
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	refer := map[string]any{"type": int64(34), "chatusr": "fixture_member", "fromusr": talker, "svrid": "101", "createtime": int64(100), "content_parsed": parsed}
	hints := quoteXMLReadHints(s, wcdb.Row{"talker": talker}, map[string]any{"refermsg": refer})
	if len(hints) != 1 || hints[0]["source"] != "media_0_voiceinfo" || mapStringAny(hints[0]["transcript"])["text"] != "fixture voice" {
		t.Fatalf("group voice quote looked up sender as conversation: %#v", hints)
	}
}

func TestTimelineFiltersCoverRecognizedAppSubtypes(t *testing.T) {
	s, talker := readMessageFixture(t)
	db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "message", "message_0.db"), s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for subtype, kind := range wxkind.AppSubtypeNames {
		id := int64(subtype + 100)
		if err := db.ExecArgs("INSERT INTO "+quoteIdent("Msg_"+talkerHash(talker))+" VALUES (?, ?, ?, ?, 1, ?, 0, ?, '')", id, id+10000, packedLocalType(49, subtype), id, id, fmt.Sprintf("<msg><appmsg><type>%d</type><title>fixture</title></appmsg></msg>", subtype)); err != nil {
			t.Fatal(err)
		}
		t.Run(fmt.Sprintf("%s_%d", kind, subtype), func(t *testing.T) {
			result, err := s.toolChatTimeline(map[string]any{"talker": talker, "type": kind, "include_media_paths": false})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range mapSliceAny(result.(map[string]any)["messages"]) {
				if int64MapValue(mapStringAny(row["id"]), "local_id") == id {
					return
				}
			}
			t.Fatalf("recognized %s subtype %d is missing from its own type filter", kind, subtype)
		})
	}
}

func TestTextPrefixRemovalRequiresMatchingSenderAndPreservesWhitespace(t *testing.T) {
	for _, tc := range []struct{ raw, sender, want string }{
		{"Note:\n  keep indentation", "fixture_sender", "Note:\n  keep indentation"},
		{"fixture_sender:\n  keep indentation", "fixture_sender", "  keep indentation"},
		{"fixture_sender:\r\n\nbody", "fixture_sender", "\nbody"},
		{"wxid_someone:\nbody", "fixture_sender", "wxid_someone:\nbody"},
		{"Subject:\nbody", "", "Subject:\nbody"},
	} {
		rows := enrichMessages([]wcdb.Row{{"local_type": int64(1), "sender_wxid": tc.sender, "message_content": tc.raw}})
		if got := rowString(rows[0], "content_summary"); got != tc.want {
			t.Errorf("timeline text %q sender %q = %q, want %q", tc.raw, tc.sender, got, tc.want)
		}
		searchRows := []wcdb.Row{{"sender_wxid": tc.sender, "content": tc.raw}}
		decorateMessageSearchRows(searchRows)
		if got := rowString(searchRows[0], "content"); got != tc.want {
			t.Errorf("search text %q sender %q = %q, want %q", tc.raw, tc.sender, got, tc.want)
		}
	}
	quote := enrichMessages([]wcdb.Row{{"local_type": packedLocalType(49, 57), "message_content": `<msg><appmsg><type>57</type><title>reply</title><refermsg><type>1</type><fromusr>fixture_sender</fromusr><content>Note:
  body</content></refermsg></appmsg></msg>`}})[0]
	if got := agentQuote(quote, nil)["text"]; got != "Note:\n  body" {
		t.Fatalf("quoted text lost its first line: %q", got)
	}
}

func reusedLocalIDFixture(t *testing.T) (*server, string) {
	t.Helper()
	s, talker := readMessageFixture(t)
	addReadFixtureDB(t, s, "message/message_1.db", fmt.Sprintf(`
		CREATE TABLE Name2Id (user_name TEXT);
		INSERT INTO Name2Id VALUES ('fixture_sender');
		CREATE TABLE %s (local_id INTEGER, server_id INTEGER, local_type INTEGER,
			sort_seq INTEGER, real_sender_id INTEGER, create_time INTEGER, status INTEGER,
			message_content TEXT, source TEXT);
		INSERT INTO %s VALUES (1,201,1,500,1,500,0,'newer theirs','');`,
		quoteIdent("Msg_"+talkerHash(talker)), quoteIdent("Msg_"+talkerHash(talker))))
	addReadFixtureDB(t, s, "message/message_fts.db", fmt.Sprintf(`
		CREATE TABLE name2id (username TEXT);
		INSERT INTO name2id VALUES ('%s');
		CREATE TABLE message_fts_v4_0_content (c0 TEXT, c1 INTEGER, c4 INTEGER, c6 INTEGER);
		INSERT INTO message_fts_v4_0_content VALUES ('hit mine',1,1,100), ('hit theirs',1,1,500);`, talker))
	return s, talker
}

func TestCrossShardSearchMatchesTimestampAndKeepsExactContext(t *testing.T) {
	s, talker := reusedLocalIDFixture(t)
	raw, err := s.toolSearchWithContext(map[string]any{"keyword": "hit", "talker": talker, "include_media_paths": false, "before_count": 0, "after_count": 0})
	if err != nil {
		t.Fatal(err)
	}
	hits := mapSliceAny(raw.(map[string]any)["hits"])
	if len(hits) != 2 {
		t.Fatalf("hits = %#v", hits)
	}
	for i, want := range []struct{ sender, serverID, text string }{{"fixture_sender", "201", "newer theirs"}, {"fixture_account", "101", "mine one"}} {
		message := mapStringAny(hits[i]["message"])
		if message["sender_wxid"] != want.sender {
			t.Errorf("hit %d attached another shard's sender: %#v", i, message)
		}
		context := mapStringAny(hits[i]["context"])
		messages := mapSliceAny(context["messages"])
		if len(messages) != 1 || messages[0]["text"] != want.text || mapStringAny(messages[0]["id"])["server_id_str"] != want.serverID {
			t.Errorf("hit %d expanded wrong context: %#v", i, hits[i])
		}
	}
}

func TestContextRejectsAmbiguousLocalIDAndAcceptsExactPair(t *testing.T) {
	s, talker := reusedLocalIDFixture(t)
	args := map[string]any{"talker": talker, "local_id": int64(1), "include_media_paths": false, "before_count": 0, "after_count": 0}
	if _, err := s.toolMessageContext(args); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("reused local_id silently selected a shard: %v", err)
	}
	args["server_id_str"] = "101"
	raw, err := s.toolMessageContext(args)
	if err != nil {
		t.Fatal(err)
	}
	messages := mapSliceAny(raw.(map[string]any)["messages"])
	if len(messages) != 1 || messages[0]["text"] != "mine one" {
		t.Fatalf("exact anchor pair ignored server id: %#v", messages)
	}
}

func TestContextRolesDoNotCollideOnReusedLocalID(t *testing.T) {
	s, talker := reusedLocalIDFixture(t)
	raw, err := s.toolMessageContext(map[string]any{"talker": talker, "server_id_str": "201", "include_media_paths": false, "before_count": 10, "after_count": 0})
	if err != nil {
		t.Fatal(err)
	}
	messages := mapSliceAny(raw.(map[string]any)["messages"])
	for _, message := range messages {
		want := "before"
		if mapStringAny(message["id"])["server_id_str"] == "201" {
			want = "anchor"
		}
		if message["context_role"] != want {
			t.Errorf("context role collision: %#v, want %s", message, want)
		}
	}
}

func TestTailAndTimelineCursorsTraverseReusedLocalIDs(t *testing.T) {
	s, talker := reusedLocalIDFixture(t)
	args := map[string]any{"talker": talker, "since_time": "100", "limit": 1, "include_media_paths": false}
	for _, want := range []string{"101", "102", "103", "104", "201"} {
		raw, err := s.toolReadEvents(args)
		if err != nil {
			t.Fatal(err)
		}
		env := raw.(map[string]any)
		events := mapSliceAny(env["events"])
		if len(events) != 1 || mapStringAny(mapStringAny(events[0]["message"])["id"])["server_id_str"] != want {
			t.Fatalf("tail skipped/repeated message %s: %#v", want, events)
		}
		args["cursor"] = env["cursor"]
	}
	args = map[string]any{"talker": talker, "order": "asc", "limit": 1, "include_media_paths": false}
	for _, want := range []string{"101", "102", "103", "104", "201"} {
		raw, err := s.toolChatTimeline(args)
		if err != nil {
			t.Fatal(err)
		}
		env := raw.(map[string]any)
		messages := mapSliceAny(env["messages"])
		if len(messages) != 1 || mapStringAny(messages[0]["id"])["server_id_str"] != want {
			t.Fatalf("timeline skipped/repeated message %s: %#v", want, messages)
		}
		cursor := mapStringAny(mapStringAny(env["query"])["cursor"])
		if cursor["next_after_server_id_str"] != want {
			t.Fatalf("timeline did not expose an unambiguous cursor: %#v", cursor)
		}
		args["after_server_id_str"] = cursor["next_after_server_id_str"]
	}
}

func TestTimelineCursorBreaksCrossShardSortTies(t *testing.T) {
	s, talker := reusedLocalIDFixture(t)
	db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "message", "message_1.db"), s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE " + quoteIdent("Msg_"+talkerHash(talker)) + " SET sort_seq=100,create_time=100"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	for _, tc := range []struct {
		order, boundary, cursor string
		want                    []string
	}{
		{"asc", "after_server_id_str", "next_after_server_id_str", []string{"101", "201", "102", "103", "104"}},
		{"desc", "before_server_id_str", "next_before_server_id_str", []string{"104", "103", "102", "201", "101"}},
	} {
		args := map[string]any{"talker": talker, "order": tc.order, "limit": 1, "include_media_paths": false}
		for _, want := range tc.want {
			raw, err := s.toolChatTimeline(args)
			if err != nil {
				t.Fatal(err)
			}
			env := raw.(map[string]any)
			messages := mapSliceAny(env["messages"])
			if len(messages) != 1 || getStr(mapStringAny(messages[0]["id"]), "server_id_str") != want {
				t.Fatalf("%s keyset skipped tied message %s: %#v", tc.order, want, messages)
			}
			args[tc.boundary] = mapStringAny(mapStringAny(env["query"])["cursor"])[tc.cursor]
		}
	}
	raw, err := s.toolMessageContext(map[string]any{"talker": talker, "server_id_str": "201", "before_count": 1, "after_count": 1, "include_media_paths": false})
	if err != nil {
		t.Fatal(err)
	}
	rows := mapSliceAny(raw.(map[string]any)["messages"])
	if len(rows) != 3 || getStr(mapStringAny(rows[0]["id"]), "server_id_str") != "101" || getStr(mapStringAny(rows[2]["id"]), "server_id_str") != "102" {
		t.Fatalf("context omitted tied neighbor: %#v", rows)
	}
	for i, want := range []string{"before", "anchor", "after"} {
		if rows[i]["context_role"] != want {
			t.Fatalf("tied context role=%v, want %s", rows[i]["context_role"], want)
		}
	}
	tailArgs := map[string]any{"talker": talker, "since_time": "100", "limit": 1, "include_media_paths": false}
	for _, want := range []string{"101", "201", "102", "103", "104"} {
		raw, err := s.toolReadEvents(tailArgs)
		if err != nil {
			t.Fatal(err)
		}
		env := raw.(map[string]any)
		events := mapSliceAny(env["events"])
		if len(events) != 1 || getStr(mapStringAny(mapStringAny(events[0]["message"])["id"]), "server_id_str") != want {
			t.Fatalf("tail skipped tied message %s: %#v", want, events)
		}
		tailArgs["cursor"] = env["cursor"]
	}
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	for _, want := range []string{"101", "201", "102", "103", "104"} {
		raw, err := s.toolDigestSource(map[string]any{"talker": talker, "since_last": true, "limit": 1, "include_media_paths": false})
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(getStr(raw.(map[string]any), "source_json"))
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		messages := mapSliceAny(payload["messages"])
		if len(messages) != 1 || getStr(mapStringAny(messages[0]["id"]), "server_id_str") != want {
			t.Fatalf("digest checkpoint skipped tied message %s: %#v", want, messages)
		}
	}
}

func TestTimelineZeroSortSequenceKeepsSQLCursorOrder(t *testing.T) {
	s, talker := readMessageFixture(t)
	db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "message", "message_0.db"), s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE " + quoteIdent("Msg_"+talkerHash(talker)) + " SET sort_seq=0,create_time=1000-create_time"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	args := map[string]any{"talker": talker, "order": "asc", "limit": 2, "include_media_paths": false}
	for _, want := range [][]string{{"101", "102"}, {"103", "104"}} {
		raw, err := s.toolChatTimeline(args)
		if err != nil {
			t.Fatal(err)
		}
		env := raw.(map[string]any)
		rows := mapSliceAny(env["messages"])
		if len(rows) != len(want) {
			t.Fatalf("zero sort sequence page: %#v", rows)
		}
		for i, id := range want {
			if getStr(mapStringAny(rows[i]["id"]), "server_id_str") != id {
				t.Fatalf("zero sort sequence changed SQL order: %#v", rows)
			}
		}
		args["after_server_id_str"] = mapStringAny(mapStringAny(env["query"])["cursor"])["next_after_server_id_str"]
	}
}

func readMessageFixture(t *testing.T) (*server, string) {
	t.Helper()
	s, _ := liveSessionFixture(t)
	talker := "fixture_group@chatroom"
	addReadFixtureDB(t, s, "message/message_0.db", fmt.Sprintf(`
		CREATE TABLE Name2Id (user_name TEXT);
		INSERT INTO Name2Id VALUES ('fixture_account'), ('fixture_sender');
		CREATE TABLE %s (local_id INTEGER, server_id INTEGER, local_type INTEGER,
			sort_seq INTEGER, real_sender_id INTEGER, create_time INTEGER, status INTEGER,
			message_content TEXT, source TEXT);
		INSERT INTO %s VALUES
		(1,101,1,100,1,100,0,'mine one',''),
		(2,102,1,200,2,200,0,'theirs two',''),
		(3,103,1,300,1,300,0,'mine three',''),
		(4,104,1,400,2,400,0,'theirs four','');`,
		quoteIdent("Msg_"+talkerHash(talker)), quoteIdent("Msg_"+talkerHash(talker))))
	return s, talker
}

func TestTimelineAndTailHonorFromMeWithoutSender(t *testing.T) {
	s, talker := readMessageFixture(t)
	args := map[string]any{"talker": talker, "from_me": true, "include_media_paths": false}
	for _, read := range []struct {
		name string
		fn   func(map[string]any) (any, error)
	}{{"timeline", s.toolChatTimeline}, {"history", s.toolMessages}, {"tail", s.toolReadEvents}} {
		t.Run(read.name, func(t *testing.T) {
			args["view"] = "agent"
			result, err := read.fn(args)
			if err != nil {
				t.Fatal(err)
			}
			env := result.(map[string]any)
			messages := mapSliceAny(env["messages"])
			if read.name == "tail" {
				for _, event := range mapSliceAny(env["events"]) {
					messages = append(messages, mapStringAny(event["message"]))
				}
			}
			if len(messages) != 2 {
				t.Fatalf("from_me returned %d rows, want only 2 self messages", len(messages))
			}
			for _, msg := range messages {
				if msg["sender_wxid"] != "fixture_account" || msg["is_from_me"] != true {
					t.Fatalf("from_me included another sender: %#v", msg)
				}
			}
		})
	}
}

func TestTailCursorSupersedesInitialStartAcrossPolls(t *testing.T) {
	s, talker := readMessageFixture(t)
	for _, start := range []map[string]any{{"since_local_id": int64(1)}, {"since_time": "200"}, {"since": "200"}, {"after": "200"}} {
		args := copyToolArgs(start)
		args["talker"], args["limit"], args["include_media_paths"] = talker, int64(1), false
		for _, expected := range []int64{2, 3, 4} {
			raw, err := s.toolReadEvents(args)
			if err != nil {
				t.Fatal(err)
			}
			env := raw.(map[string]any)
			events := mapSliceAny(env["events"])
			if len(events) != 1 {
				t.Fatalf("start=%v expected message %d, events=%#v", start, expected, events)
			}
			id := mapStringAny(mapStringAny(events[0]["message"])["id"])
			if got := int64MapValue(id, "local_id"); got != expected {
				t.Fatalf("start=%v next message=%d, want %d; cursor did not advance", start, got, expected)
			}
			args["cursor"] = env["cursor"]
		}
	}
}

func TestSenderNamesUseLiveContactsAndRejectAmbiguity(t *testing.T) {
	s, _ := liveSessionFixture(t)
	got, err := s.resolveLooseSenderArg(map[string]any{"sender": "Friend"})
	if err != nil || got != "fixture_friend" {
		t.Fatalf("live sender missing from stale cache: %q, %v", got, err)
	}
	db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "contact", "contact.db"), s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Exec(`INSERT INTO contact VALUES ('another_friend','Friend','','',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resolveLooseSenderArg(map[string]any{"sender": "Friend"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous sender silently selected: %v", err)
	}
}

func TestSelfWxidOnlyStripsHexDeviceSuffix(t *testing.T) {
	for _, tc := range []struct{ account, directory, want string }{
		{"wxid_owner_ab12", "wxid_owner_ab12", "wxid_owner"},
		{"wxid_owner_AB12", "wxid_owner_AB12", "wxid_owner"},
		{"wxid_mike", "wxid_mike", "wxid_mike"},
		{"wxid_owner_name", "wxid_owner_name", "wxid_owner_name"},
		{"wxid_dead", "wxid_dead_ab12", "wxid_dead"},
		{"wxid_owner_ab12", "wxid_owner_ab12_cd34", "wxid_owner_ab12"},
		{"wxid_dead", "unrelated-directory", "wxid_dead"},
		{"wxid_dead", "", "wxid_dead"},
	} {
		root := ""
		if tc.directory != "" {
			root = filepath.Join(t.TempDir(), tc.directory)
		}
		s := &server{cfg: &config.Config{Wxid: tc.account, DBRoot: root}}
		if got := s.selfWxid(); got != tc.want {
			t.Errorf("selfWxid(%q)=%q, want %q", tc.account, got, tc.want)
		}
	}
}

func TestSearchReportsFTSHitsMissingLiveMessageMetadata(t *testing.T) {
	s, talker := readMessageFixture(t)
	addReadFixtureDB(t, s, "message/message_fts.db", fmt.Sprintf(`
		CREATE TABLE name2id (username TEXT);
		INSERT INTO name2id VALUES ('%s');
		CREATE TABLE message_fts_v4_0_content (c0 TEXT, c1 INTEGER, c4 INTEGER, c6 INTEGER);
		INSERT INTO message_fts_v4_0_content VALUES
		('mine one',1,1,100), ('mine deleted',99,1,500);`, talker))
	raw, err := s.toolSearch(map[string]any{"keyword": "mine", "talker": talker})
	if err != nil {
		t.Fatal(err)
	}
	result := raw.(cliRowsResult)
	if len(result.Rows) != 2 || result.Freshness["complete"] != false || !strings.Contains(strings.Join(result.Warnings, " "), "missing_message_metadata") {
		t.Fatalf("missing live rows reported as complete: %#v", result)
	}
	if _, err := s.toolSearch(map[string]any{"keyword": "mine", "talker": talker, "from_me": true}); err == nil || !strings.Contains(err.Error(), "completeness") {
		t.Fatalf("sender filter silently discarded an unverifiable hit: %v", err)
	}
}

func TestSearchEnrichmentHandlesLargeChatBatch(t *testing.T) {
	s, talker := readMessageFixture(t)
	db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "message", "message_0.db"), s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Exec(fmt.Sprintf(`WITH RECURSIVE ids(n) AS (VALUES(5) UNION ALL SELECT n+1 FROM ids WHERE n < 1201)
		INSERT INTO %s SELECT n, n+1000, 1, n*100, 1, n*100, 0, 'mine', '' FROM ids`, quoteIdent("Msg_"+talkerHash(talker)))); err != nil {
		t.Fatal(err)
	}
	rows := make([]wcdb.Row, 1201)
	for i := range rows {
		rows[i] = wcdb.Row{"talker": talker, "local_id": int64(i + 1)}
	}
	if warnings := s.enrichSearchSender(rows); len(warnings) != 0 {
		t.Fatalf("large chat batch could not be enriched: %v", warnings)
	}
	for _, row := range rows {
		if rowString(row, "sender_wxid") == "" || rowString(row, "kind_name") != "text" {
			t.Fatalf("large chat batch missed metadata: %#v", row)
		}
	}
}

func TestTimelineSenderLookupFailureIsNotEmptySuccess(t *testing.T) {
	s, talker := readMessageFixture(t)
	db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "message", "message_0.db"), s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP TABLE Name2Id"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	context, err := s.toolMessageContext(map[string]any{"talker": talker, "server_id_str": "101", "include_media_paths": false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(stringSliceAny(context.(map[string]any)["warnings"]), " "), "sender_metadata_unavailable") {
		t.Fatalf("context sender lookup failure lost diagnostics: %#v", context)
	}
	args := map[string]any{"talker": talker, "from_me": true, "include_media_paths": false}
	if _, err := s.toolChatTimeline(args); err == nil || !strings.Contains(err.Error(), "sender") {
		t.Fatalf("unavailable sender metadata returned empty success: %v", err)
	}
	delete(args, "from_me")
	raw, err := s.toolChatTimeline(args)
	if err != nil {
		t.Fatal(err)
	}
	env := raw.(map[string]any)
	if !strings.Contains(strings.Join(stringSliceAny(env["warnings"]), " "), "sender_metadata_unavailable") {
		t.Fatalf("sender lookup failure lost diagnostics: %#v", env["warnings"])
	}
	for _, msg := range mapSliceAny(env["messages"]) {
		if _, ok := msg["is_from_me"]; ok {
			t.Fatalf("unknown sender reported as known ownership: %#v", msg)
		}
	}
}
