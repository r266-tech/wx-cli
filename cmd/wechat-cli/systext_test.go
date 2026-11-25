package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/diagnostics"
)

func TestRenderSystemText(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"plain", `"A" 撤回了一条消息`, `"A" 撤回了一条消息`},
		{"join by qr", "123@chatroom:\n<sysmsg type=\"sysmsgtemplate\"><sysmsgtemplate><content_template type=\"tmpl_type_profile\"><plain><![CDATA[]]></plain><template><![CDATA[\"$adder$\"通过扫描\"$from$\"分享的二维码加入群聊]]></template><link_list><link name=\"adder\" type=\"link_profile\"><memberlist><member><username><![CDATA[wxid_a]]></username><nickname><![CDATA[阿甲]]></nickname></member></memberlist></link><link name=\"from\" type=\"link_profile\"><memberlist><member><username><![CDATA[wxid_b]]></username><nickname><![CDATA[阿乙]]></nickname></member></memberlist></link></link_list></content_template></sysmsgtemplate></sysmsg>",
			`"阿甲"通过扫描"阿乙"分享的二维码加入群聊`},
		{"invite with hidden revoke", "123@chatroom:\n<sysmsg type=\"sysmsgtemplate\"><sysmsgtemplate><content_template type=\"tmpl_type_profilewithrevoke\"><template><![CDATA[你邀请\"$names$\"加入了群聊  $revoke$]]></template><link_list><link name=\"names\" type=\"link_profile\"><memberlist><member><nickname><![CDATA[甲]]></nickname></member><member><nickname><![CDATA[乙]]></nickname></member></memberlist><separator><![CDATA[、]]></separator></link><link name=\"revoke\" type=\"link_revoke\" hidden=\"1\"><title><![CDATA[撤销]]></title></link></link_list></content_template></sysmsgtemplate></sysmsg>",
			`你邀请"甲、乙"加入了群聊`},
		{"pin", "123@chatroom:\n<sysmsg type=\"mmchatroomtopmsg\"><mmchatroomtopmsg><op><![CDATA[1]]></op><nickname><![CDATA[x]]></nickname></mmchatroomtopmsg></sysmsg>", "[群置顶] 置顶了一条消息"},
		{"hongbao markup", `<img src="SystemMessages_HongbaoIcon.png"/>  你领取了$wxid_abc$的<_wc_custom_link_ color="#FD9931" href="weixin://x">红包</_wc_custom_link_>`, "你领取了wxid_abc的红包"},
		{"paymsg", `<?xml version="1.0"?><sysmsg type="paymsg"><content><![CDATA[你有一笔待接收的<_wc_custom_link_ href="weixin://wxpay/x">转账</_wc_custom_link_>]]></content></sysmsg>`, "你有一笔待接收的转账"},
		{"revoke", `<sysmsg type="revokemsg"><revokemsg><content><![CDATA["A" 撤回了一条消息]]></content></revokemsg></sysmsg>`, `"A" 撤回了一条消息`},
		{"unknown kept", `<sysmsg type="future"><x>1</x></sysmsg>`, `<sysmsg type="future"><x>1</x></sysmsg>`},
	}
	for _, c := range cases {
		if got := renderSystemText(c.raw); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCLIShorthandMapsContextWindow(t *testing.T) {
	flags := parseKVFlags([]string{"--local-id", "1", "--before", "3", "--after", "4"})
	applyCLIShorthand("message_context", flags)
	if flags["before_count"] == nil || flags["after_count"] == nil || flags["before"] != nil {
		t.Fatalf("shorthand not mapped: %#v", flags)
	}
	if err := validateToolArgs("message_context", flags); err != nil {
		t.Fatalf("mapped flags rejected: %v", err)
	}
	events := map[string]any{"after": "x"}
	applyCLIShorthand("read_events", events)
	if events["after"] != "x" {
		t.Fatalf("read_events after must be untouched: %#v", events)
	}
}

func TestChatAmbiguousErrorCarriesCandidates(t *testing.T) {
	var err error = &chatAmbiguousError{Query: "小明", Candidates: []chatCandidate{
		{Username: "wxid_a", DisplayName: "小明同学", ChatType: "private"},
		{Username: "wxid_b", DisplayName: "王小明", ChatType: "private"},
	}}
	if code := toolErrorCode(err); code != "chat_ambiguous" {
		t.Fatalf("code = %q", code)
	}
	got := cliErrorCandidates(fmt.Errorf("wrapped: %w", err))
	if len(got) != 2 || got[0].ID != "wxid_a" || got[1].DisplayName != "王小明" {
		t.Fatalf("candidates = %#v", got)
	}
	if cliErrorCandidates(fmt.Errorf("other")) != nil {
		t.Fatal("plain errors must carry no candidates")
	}
}

func TestCLIShorthandLeavesSearchContextTimeFilters(t *testing.T) {
	flags := map[string]any{"before": "2026-01-01", "after": "2025-01-01"}
	applyCLIShorthand("search_with_context", flags)
	if flags["before"] != "2026-01-01" || flags["after"] != "2025-01-01" || flags["before_count"] != nil {
		t.Fatalf("search_with_context time filters changed: %#v", flags)
	}
}

func TestAmbiguousEnvelopeExposesCandidateIDs(t *testing.T) {
	err := &chatAmbiguousError{Query: "小明", Candidates: []chatCandidate{{Username: "wxid_abc", DisplayName: "小明同学", ChatType: "private"}}}
	raw, _ := json.Marshal(cliErrorEnvelope{Error: cliError{Code: "chat_ambiguous", Message: diagnostics.Redact(err.Error()), Candidates: cliErrorCandidates(err)}})
	var env struct {
		Error struct {
			Message    string
			Candidates []struct{ ID string }
		}
	}
	if json.Unmarshal(raw, &env) != nil || len(env.Error.Candidates) != 1 || env.Error.Candidates[0].ID != "wxid_abc" {
		t.Fatalf("envelope = %s", raw)
	}
	if strings.Contains(env.Error.Message, "wxid_abc") {
		t.Fatalf("message should stay redacted: %s", env.Error.Message)
	}
}
