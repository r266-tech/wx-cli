package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/r266-tech/wx-cli/internal/config"
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
		if len(commands) != 2 || commands[1] != `repeat with --before-message <data.query.cursor.next_before_message> while data.query.has_more` {
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
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("digest state permissions = %v %v", info, err)
	}
}
