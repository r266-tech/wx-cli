package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/config"
)

func TestReadOSStatusHidesAccountIdentifierUnlessDebug(t *testing.T) {
	s := &server{cfg: &config.Config{
		Wxid:   "wxid_private_account",
		DBRoot: t.TempDir(),
		Keys:   map[string]string{"salt": "key"},
	}}

	status := s.readOSStatus(false)
	account, _ := status["account"].(map[string]any)
	if account["wxid"] != nil {
		t.Fatalf("default status exposed wxid: %#v", account)
	}
	if account["identity_configured"] != true {
		t.Fatalf("identity_configured = %#v, want true", account["identity_configured"])
	}

	debugStatus := s.readOSStatus(true)
	debug, _ := debugStatus["account_debug"].(map[string]any)
	if debug["wxid"] != "wxid_private_account" {
		t.Fatalf("debug wxid = %#v", debug["wxid"])
	}
}

func TestReadOSFullChatWorkflowUsesStableCursor(t *testing.T) {
	for _, workflow := range readOSWorkflows() {
		if workflow["name"] != "page_full_chat" {
			continue
		}
		commands, _ := workflow["commands"].([]string)
		if len(commands) != 2 || commands[1] != `repeat with --before-message-token <data.query.cursor.next_before_message_token> while data.query.has_more` {
			t.Fatalf("page_full_chat commands = %#v", commands)
		}
		return
	}
	t.Fatal("page_full_chat workflow not found")
}

func TestReadOSDoctorReportsCurrentExecutableAndWarnings(t *testing.T) {
	t.Setenv("PATH", filepath.Dir(os.Args[0]))
	doctor := (&server{}).readOSDoctor()
	runtimeInfo, ok := doctor["runtime"].(map[string]any)
	if !ok || runtimeInfo["app_version"] != appVersion {
		t.Fatalf("runtime provenance = %#v", runtimeInfo)
	}
	if runtimeInfo["current_executable"] == "" {
		t.Fatalf("doctor omitted current executable: %#v", runtimeInfo)
	}
	if _, ok := doctor["components"].(map[string]any); !ok {
		t.Fatalf("doctor omitted components: %#v", doctor)
	}
}

func TestDigestHelpersSortCountsAndUsePrivateAtomicState(t *testing.T) {
	counts := sortedCounts(map[string]int{"b": 1, "a": 2, "c": 2})
	if counts[0]["name"] != "a" || counts[1]["name"] != "c" || counts[2]["name"] != "b" {
		t.Fatalf("sorted counts = %#v", counts)
	}
	root := t.TempDir()
	if err := saveDigestState(filepath.Join(root, "chat"), digestCursor{Timestamp: 42, LocalID: 7}); err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(filepath.Join(root, "chat", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(state) == "" {
		t.Fatal("empty digest state")
	}
	info, err := os.Stat(filepath.Join(root, "chat", "state.json"))
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("digest state permissions = %v %v", info, err)
	}
}

func TestAssistantSchemasExposeStableObservationInputs(t *testing.T) {
	for _, fixture := range []struct {
		command    string
		properties []string
	}{
		{"timeline", []string{"before_message_token", "after_message_token"}},
		{"history", []string{"before_message_token", "after_message_token"}},
		{"context", []string{"message_token"}},
		{"tail", []string{"cursor", "cursor_storage", "scan_limit", "type_filter", "keyword", "unread_only"}},
	} {
		t.Run(fixture.command, func(t *testing.T) {
			_, tool, ok := cliHelpForTarget(fixture.command)
			if !ok {
				t.Fatal("tool not found")
			}
			tool = displayToolDef(tool)
			properties := toolInputProperties(tool)
			for _, name := range fixture.properties {
				property, exists := properties[name]
				if !exists || property == nil {
					t.Fatalf("assistant schema hides %s", name)
				}
			}
			if fixture.command == "tail" {
				if tool.LocalWriteMode != "possible" || tool.StrictReadOnlyBehavior != "allowed_without_writes" {
					t.Fatalf("session cursor storage contract = %#v", tool)
				}
				if got := properties["scan_limit"].(map[string]any)["maximum"]; got != int64(100000) {
					t.Fatalf("session scan maximum = %v", got)
				}
				values := properties["cursor_storage"].(map[string]any)["enum"].([]string)
				if strings.Join(values, ",") != "auto,local,inline" {
					t.Fatalf("cursor storage = %v", values)
				}
			}
		})
	}
}

func TestReadOSExplainsSessionObservationBoundary(t *testing.T) {
	for _, entry := range readOSEntrypoints() {
		if entry["command"] != "tail" {
			continue
		}
		use, _ := entry["use"].(string)
		if !strings.Contains(use, "unread-only changes") || !strings.Contains(use, "not a complete event log") {
			t.Fatalf("session observation contract = %q", use)
		}
		return
	}
	t.Fatal("tail entrypoint not found")
}
