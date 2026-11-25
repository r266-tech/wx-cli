package main

import (
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

func TestCLISessionRowsDropInternalFields(t *testing.T) {
	rows := []map[string]any{
		{"username": "a", "last_msg_kind_name": "text", "last_msg_type": int64(1), "last_msg_sub_type": int64(0), "last_timestamp": int64(10), "sort_timestamp": int64(10)},
		{"username": "b", "last_timestamp": int64(10), "sort_timestamp": int64(20)},
	}
	out := cliResultRowsForTool("unread", nil, rows)
	if _, ok := out[0]["last_msg_type"]; ok {
		t.Fatalf("raw type codes kept: %#v", out[0])
	}
	if _, ok := out[0]["sort_timestamp"]; ok {
		t.Fatalf("duplicate sort_timestamp kept: %#v", out[0])
	}
	if out[1]["sort_timestamp"] != int64(20) {
		t.Fatalf("distinct sort_timestamp must stay: %#v", out[1])
	}
	if rows[0]["last_msg_type"] == nil || rows[0]["sort_timestamp"] == nil {
		t.Fatal("internal rows must stay unshaped")
	}
}

func TestAgentMessageCarriesOneTimeString(t *testing.T) {
	m := agentMessage(map[string]any{"create_time": int64(1776330000), "sender_display_name": "甲", "sender_wxid": "wxid_a", "kind_name": "text", "content_summary": "hi"})
	if _, ok := m["time"]; ok {
		t.Fatalf("time duplicates time_iso: %#v", m)
	}
	if m["time_iso"] == "" || m["create_time"] != int64(1776330000) {
		t.Fatalf("time fields missing: %#v", m)
	}
	if got := agentMarkdownTime(m); got == "" {
		t.Fatal("markdown export lost its timestamp")
	}
}

func TestSystemTextWxidsBecomeNames(t *testing.T) {
	rows := []wcdb.Row{
		{"base_kind": int64(10000), "talker": "r@chatroom", "message_content": "r@chatroom:\n你领取了$wxid_a$的<_wc_custom_link_ href=\"x\">红包</_wc_custom_link_>", "content_summary": "你领取了wxid_a的红包"},
		{"base_kind": int64(10000), "talker": "wxid_b", "message_content": "$wxid_b$领取了你的红包", "content_summary": "wxid_b领取了你的红包"},
		{"base_kind": int64(1), "talker": "wxid_b", "message_content": "$wxid_b$", "content_summary": "$wxid_b$"},
	}
	rooms := func(map[string]map[string]bool) map[string]map[string]string {
		return map[string]map[string]string{"r@chatroom": {"wxid_a": "群名片甲"}}
	}
	contacts := func(map[string]bool) map[string]string { return map[string]string{"wxid_a": "甲", "wxid_b": "乙"} }
	attachSystemTextNames(rows, rooms, contacts)
	if rows[0]["content_summary"] != "你领取了群名片甲的红包" || rows[1]["content_summary"] != "乙领取了你的红包" || rows[2]["content_summary"] != "$wxid_b$" {
		t.Fatalf("got %q / %q / %q", rows[0]["content_summary"], rows[1]["content_summary"], rows[2]["content_summary"])
	}
}
