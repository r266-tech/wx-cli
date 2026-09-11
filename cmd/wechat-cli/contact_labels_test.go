package main

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

func appendTestProtoVarintField(dst []byte, field, value uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], field<<3)
	dst = append(dst, buf[:n]...)
	n = binary.PutUvarint(buf[:], value)
	return append(dst, buf[:n]...)
}

func appendTestProtoStringField(dst []byte, field uint64, value string) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], field<<3|2)
	dst = append(dst, buf[:n]...)
	n = binary.PutUvarint(buf[:], uint64(len(value)))
	dst = append(dst, buf[:n]...)
	return append(dst, value...)
}

func TestContactLabelIDsReadsContactInfoField30(t *testing.T) {
	var extra []byte
	extra = appendTestProtoVarintField(extra, 2, 1)
	extra = appendTestProtoStringField(extra, 4, "signature")
	extra = appendTestProtoStringField(extra, contactLabelListProtoField, "5,8,5,")
	extra = appendTestProtoStringField(extra, contactLabelListProtoField, "10")

	if got, want := contactLabelIDs(extra), []int64{5, 8, 10}; !reflect.DeepEqual(got, want) {
		t.Fatalf("contactLabelIDs = %#v, want %#v", got, want)
	}
}

func TestContactLabelIDsAcceptsKnownSeparatorsAndSkipsInvalidIDs(t *testing.T) {
	extra := appendTestProtoStringField(nil, contactLabelListProtoField, "2， 5;bad|0；8")
	if got, want := contactLabelIDs(extra), []int64{2, 5, 8}; !reflect.DeepEqual(got, want) {
		t.Fatalf("contactLabelIDs = %#v, want %#v", got, want)
	}
}

func TestContactLabelIDsStopsSafelyOnMalformedProto(t *testing.T) {
	extra := appendTestProtoStringField(nil, contactLabelListProtoField, "2,5")
	extra = append(extra, byte(contactLabelListProtoField<<3|2), 0xff)
	if got, want := contactLabelIDs(extra), []int64{2, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("contactLabelIDs malformed = %#v, want parsed prefix %#v", got, want)
	}
}

func TestContactLabelRefsUseLabelSortOrderAndPreserveUnknownIDs(t *testing.T) {
	definitions := contactLabelDefinitionsByID([]contactLabelDefinition{
		{ID: 2, Name: "客户", SortOrder: 1},
		{ID: 5, Name: "朋友", SortOrder: 0},
	})
	extra := appendTestProtoStringField(nil, contactLabelListProtoField, "2,99,5")
	got := contactLabelRefs(extra, definitions)
	want := []contactLabelRef{{ID: 5, Name: "朋友"}, {ID: 2, Name: "客户"}, {ID: 99}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contactLabelRefs = %#v, want %#v", got, want)
	}
}

func TestCountContactLabelMembershipsCountsEachContactOnce(t *testing.T) {
	rows := []wcdb.Row{
		{"extra_buffer": appendTestProtoStringField(nil, contactLabelListProtoField, "2,5,2")},
		{"extra_buffer": appendTestProtoStringField(nil, contactLabelListProtoField, "5")},
		{"extra_buffer": []byte{}},
	}
	got := countContactLabelMemberships(rows)
	if got[2] != 1 || got[5] != 2 {
		t.Fatalf("countContactLabelMemberships = %#v", got)
	}
}

func TestContactLabelFilterPrefersExactNameThenContains(t *testing.T) {
	labels := []contactLabelDefinition{
		{ID: 2, Name: "客户"},
		{ID: 5, Name: "重要客户"},
		{ID: 7, Name: "校友"},
	}

	exact, active := contactLabelFilterIDs(labels, map[string]any{"label": " 客 户 "})
	if !active || !reflect.DeepEqual(exact, map[int64]bool{2: true}) {
		t.Fatalf("exact filter = %#v, active=%v", exact, active)
	}
	partial, active := contactLabelFilterIDs(labels, map[string]any{"label": "重要"})
	if !active || !reflect.DeepEqual(partial, map[int64]bool{5: true}) {
		t.Fatalf("partial filter = %#v, active=%v", partial, active)
	}
	byID, active := contactLabelFilterIDs(labels, map[string]any{"label_id": int64(7)})
	if !active || !reflect.DeepEqual(byID, map[int64]bool{7: true}) {
		t.Fatalf("id filter = %#v, active=%v", byID, active)
	}
	mismatch, active := contactLabelFilterIDs(labels, map[string]any{"label": "客户", "label_id": int64(5)})
	if !active || len(mismatch) != 0 {
		t.Fatalf("combined mismatch filter = %#v, active=%v", mismatch, active)
	}
}

func TestContactLabelToolsAreOnAssistantSurface(t *testing.T) {
	if !toolInProfile("contact_labels", "assistant") {
		t.Fatal("contact_labels missing from assistant profile")
	}
	if cliResultListKey("contact_labels") != "labels" {
		t.Fatalf("contact_labels list key = %q", cliResultListKey("contact_labels"))
	}
	if !readOSCapabilities(true, true, false)["contact_labels"] {
		t.Fatal("contact_labels missing from ready capability map")
	}
	foundContacts := false
	foundLabels := false
	for _, def := range toolDefs {
		switch def.Name {
		case "contacts":
			foundContacts = true
			schema := def.InputSchema.(map[string]any)
			properties := schema["properties"].(map[string]any)
			if properties["label"] == nil || properties["label_id"] == nil {
				t.Fatalf("contacts label filters missing: %#v", properties)
			}
		case "contact_labels":
			foundLabels = true
		}
	}
	if !foundContacts || !foundLabels {
		t.Fatalf("contact tool definitions: contacts=%v contact_labels=%v", foundContacts, foundLabels)
	}
}

func TestContactLabelCompanionCommandRouting(t *testing.T) {
	for _, command := range []string{"labels", "contact-labels", "contact_labels", "contact-tags", "contact_tags"} {
		if got := companionNormalizeWechatTool(command); got != "contact_labels" {
			t.Fatalf("companionNormalizeWechatTool(%q) = %q", command, got)
		}
	}
	if got := companionWechatDisplayCommandForTool("contact_labels", ""); got != "labels" {
		t.Fatalf("contact_labels display command = %q", got)
	}
	if got := companionToolLabel("contact_labels"); got != "读取联系人标签" {
		t.Fatalf("contact_labels companion label = %q", got)
	}
}
