package main

import (
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

func TestMarkMessageMentions(t *testing.T) {
	for _, tc := range []struct {
		source  string
		me, all bool
	}{
		{`<msgsource><atuserlist><![CDATA[wxid_other,wxid_me]]></atuserlist></msgsource>`, true, false},
		{`<msgsource><atuserlist>notify@all</atuserlist></msgsource>`, false, true},
		{`<msgsource><atuserlist><![CDATA[wxid_other]]></atuserlist></msgsource>`, false, false},
		{`<msgsource><silence>1</silence></msgsource>`, false, false},
	} {
		r := wcdb.Row{"source": tc.source}
		markMessageMentions(r, "wxid_me")
		if me, _ := r["mentions_me"].(bool); me != tc.me {
			t.Errorf("%s: mentions_me=%v", tc.source, me)
		}
		if all, _ := r["mentions_all"].(bool); all != tc.all {
			t.Errorf("%s: mentions_all=%v", tc.source, all)
		}
	}
	r := wcdb.Row{"source": `<atuserlist>wxid_me</atuserlist>`}
	markMessageMentions(r, "")
	if r["mentions_me"] != nil {
		t.Fatal("an unknown self must never match")
	}
}
