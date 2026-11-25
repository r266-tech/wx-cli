package main

import (
	"regexp"
	"strings"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

var atUserListRE = regexp.MustCompile(`(?s)<atuserlist>\s*(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?\s*</atuserlist>`)

// markMessageMentions reads the group @ list from the decoded msgsource so an
// agent can tell which messages ask for the user without guessing from text.
func markMessageMentions(r wcdb.Row, selfWxid string) {
	m := atUserListRE.FindStringSubmatch(rowString(r, "source"))
	if m == nil {
		return
	}
	for _, id := range strings.Split(m[1], ",") {
		switch id = strings.TrimSpace(id); {
		case id == "notify@all":
			r["mentions_all"] = true
		case id != "" && id == selfWxid:
			r["mentions_me"] = true
		}
	}
}
