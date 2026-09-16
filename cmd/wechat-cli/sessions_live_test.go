package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/config"
	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

func liveSessionFixture(t *testing.T) (*server, string) {
	t.Helper()
	lib, err := findWCDB()
	if err != nil {
		t.Skip("WCDB fixture library unavailable")
	}
	if err := wcdb.Bootstrap(lib); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("WECHAT_CLI_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	cfg := &config.Config{DBRoot: root, Wxid: "fixture_account", Keys: make(map[string]string)}
	create := func(subdir, schema string) {
		t.Helper()
		path := filepath.Join(root, "db_storage", subdir, subdir+".db")
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
		if err := db.Exec(schema); err != nil {
			db.Close()
			t.Fatal(err)
		}
		db.Close()
		cfg.Keys[saltHex] = keyHex
	}
	create("session", `CREATE TABLE SessionTable (
		username TEXT, unread_count INTEGER, summary TEXT, last_timestamp INTEGER,
		sort_timestamp INTEGER, last_msg_sender TEXT, last_sender_display_name TEXT,
		last_msg_type INTEGER, last_msg_sub_type INTEGER, is_hidden INTEGER);
		INSERT INTO SessionTable VALUES
		('fixture_group@chatroom',3,'fresh group',500,500,'fixture_sender','Member',1,0,0),
		('fixture_friend',0,'fresh private',400,400,'','',1,0,0),
		('fixture_official',2,'verified account',300,300,'','',1,0,0),
		('fixture_hidden@chatroom',9,'hidden',900,900,'','',1,0,1);`)
	create("contact", `CREATE TABLE contact (username TEXT, remark TEXT, nick_name TEXT, alias TEXT, verify_flag INTEGER);
		INSERT INTO contact VALUES
		('fixture_group@chatroom','','Fixture Group','group',0),
		('fixture_friend','Friend','','friend',0),
		('fixture_official','','Official','official',1);`)
	// Deliberately stale cache: old implementations would return this row first.
	paths, err := cachePathsFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.RootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := wcdb.OpenPlain(paths.IndexPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := createIndexSchema(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO sessions_unified (username, display_name, unread_count, summary, last_timestamp, sort_timestamp) VALUES ('stale@chatroom','Stale',99,'old preview',1,1)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	return &server{cfg: cfg, wcdbPath: lib, ok: true}, root
}

func fixtureFileState(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	state := make(map[string][32]byte)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state[path] = sha256.Sum256(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestSessionsAndUnreadReadLiveWithStaleCache(t *testing.T) {
	s, root := liveSessionFixture(t)
	before := fixtureFileState(t, root)
	result, err := s.toolSessions(map[string]any{"limit": 10})
	if err != nil {
		t.Fatal(err)
	}
	data := result.(cliRowsResult)
	if len(data.Rows) != 3 || rowString(data.Rows[0], "summary") != "fresh group" || data.Freshness["message_source"] != "live_session_db" {
		t.Fatalf("sessions did not read the live visible rows: %#v", data)
	}
	result, err = s.toolSessions(map[string]any{"type_filter": "private,group", "limit": 1, "offset": 1})
	if err != nil {
		t.Fatal(err)
	}
	rows := result.(cliRowsResult).Rows
	if len(rows) != 1 || rowString(rows[0], "display_name") != "Friend" {
		t.Fatalf("filtered pagination: %#v", rows)
	}
	result, err = s.toolSessions(map[string]any{"type_filter": "official_account"})
	if err != nil {
		t.Fatal(err)
	}
	rows = result.(cliRowsResult).Rows
	if len(rows) != 1 || rowString(rows[0], "display_name") != "Official" {
		t.Fatal("verified official account incorrectly filtered")
	}
	result, err = s.toolUnread(map[string]any{"filter": "group"})
	if err != nil {
		t.Fatal(err)
	}
	rows = result.(cliRowsResult).Rows
	if len(rows) != 1 || rowInt64(rows[0], "unread_count") != 3 {
		t.Fatalf("unread was stale or unfiltered: %#v", rows)
	}
	if after := fixtureFileState(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("strict live session reads changed support/source files")
	}
}

func TestReadOSProbeRejectsIncorrectRuntimeKey(t *testing.T) {
	s, _ := liveSessionFixture(t)
	if status := s.readOSStatus(false); status["live_read_ok"] != true {
		t.Fatalf("valid fixture unreadable: %#v", status)
	}
	for salt := range s.cfg.Keys {
		s.cfg.Keys[salt] = strings.Repeat("ab", 32)
	}
	status := s.readOSStatus(false)
	if status["live_read_ok"] != false || status["blocked_by"] != "live_read_probe_failed" {
		t.Fatalf("bad keys reported live readiness: %#v", status)
	}
}

func TestLiveNameResolutionDoesNotDependOnCachedContacts(t *testing.T) {
	s, _ := liveSessionFixture(t)
	result, err := s.toolResolveChat(map[string]any{"query": "Fixture Group", "type_filter": "group"})
	if err != nil {
		t.Fatal(err)
	}
	candidates := result.(map[string]any)["candidates"].([]chatCandidate)
	if len(candidates) != 1 || candidates[0].Username != "fixture_group@chatroom" || candidates[0].LastTimestamp != 500 {
		t.Fatalf("new live conversation not resolved: %#v", candidates)
	}
	paths, err := cachePathsFor(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.IndexPath); err != nil {
		t.Fatal(err)
	}
	got, err := s.resolveLooseChatArg(map[string]any{"chat": "Fixture Group"})
	if err != nil || got != "fixture_group@chatroom" {
		t.Fatal("name resolution requires a cache", err)
	}
	db, err := wcdb.OpenWithKeyMapWritable(filepath.Join(s.cfg.DBRoot, "db_storage", "contact", "contact.db"), s.cfg.Keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO contact VALUES ('other@chatroom','','Fixture Group','',0)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if _, err := s.resolveLooseChatArg(map[string]any{"chat": "Fixture Group"}); err == nil {
		t.Fatal("duplicate names were silently resolved")
	}
}

func TestQuoteUsesConversationAndSourceSender(t *testing.T) {
	room := "fixture_group@chatroom"
	source := wcdb.Row{"talker": room, "local_id": int64(10), "server_id_str": "12345", "kind_name": "text", "sender_wxid": "fixture_member", "sender_display_name": "Member", "content_summary": "fixture text"}
	quote := wcdb.Row{"talker": room, "kind_name": "quote", "message_content_parsed": map[string]any{"refermsg": map[string]any{"type": 1, "displayname": room, "fromusr": room, "chatusr": "fixture_member", "svrid": "12345", "content_raw": "fixture text"}}}
	q := agentQuote(quote, agentSourceMessageIndex([]wcdb.Row{source}))
	if q["sender"] != "Member" || q["id"].(map[string]any)["talker"] != room {
		t.Fatalf("bad group quote identity: %#v", q)
	}
	source["talker"] = "other@chatroom"
	q = agentQuote(quote, agentSourceMessageIndex([]wcdb.Row{source}))
	if q["source_id"] != nil || q["sender"] == "Member" {
		t.Fatal("quote borrowed identity from another conversation")
	}
}

func TestAssistantSessionFilterAndCursorHelp(t *testing.T) {
	_, tool, ok := cliHelpForTarget("sessions")
	if !ok || !hasAnyProp(toolInputProperties(displayToolDef(tool)), "type_filter") {
		t.Fatal("assistant schema hides session filter")
	}
	spec, tool, _ := cliHelpForTarget("timeline")
	help := agentHelpForTool(spec, displayToolDef(tool))
	strategy := strings.Join(help["strategy"].([]string), " ")
	if !strings.Contains(strategy, "next_before_message") || strings.Contains(strategy, "increment offset") {
		t.Fatal("timeline help does not use stable paging")
	}
}
