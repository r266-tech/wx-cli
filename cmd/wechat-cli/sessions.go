package main

import (
	"fmt"
	"strings"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
	"github.com/r266-tech/wx-cli/v2/internal/wxkind"
)

// Session ordering, previews, and unread counts always come from the live DB.
func (s *server) toolSessions(a map[string]any) (any, error) {
	db, err := s.openDB("session", "session.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var where []string
	var args []any
	where = append(where, "COALESCE(is_hidden, 0) = 0")
	if getBool(a, "unread_only") {
		where = append(where, "unread_count > 0")
	}
	typeFilter := getStr(a, "type_filter")
	if tf := typeFilter; tf != "" && tf != "all" {
		switch tf {
		case "group":
			where = append(where, "username LIKE '%@chatroom'")
		case "friend", "private":
			where = append(where, `username NOT LIKE '%@chatroom'
				AND username NOT LIKE 'gh!_%' ESCAPE '!'
				AND username NOT LIKE '%@openim'
				AND username NOT LIKE '%@weclaw'
				AND username NOT LIKE '%@stranger'`)
		case "bot":
			where = append(where, "username LIKE '%@weclaw'")
		}
	}
	if kw := getStr(a, "keyword"); kw != "" {
		// Cross-db: also include sessions whose talker matches display_name /
		// nick_name / remark / alias in contact.db (fuzzy, case+space insensitive).
		matched := s.findUsernamesByFuzzyName(kw)
		clauses := []string{"username LIKE ? COLLATE NOCASE", "summary LIKE ? COLLATE NOCASE"}
		like := "%" + kw + "%"
		args = append(args, like, like)
		if len(matched) > 0 {
			ph := make([]string, len(matched))
			for i, u := range matched {
				ph[i] = "?"
				args = append(args, u)
			}
			clauses = append(clauses, fmt.Sprintf("username IN (%s)", strings.Join(ph, ",")))
		}
		where = append(where, "("+strings.Join(clauses, " OR ")+")")
	}
	query := fmt.Sprintf(`SELECT username, unread_count, summary,
		last_timestamp, sort_timestamp,
		last_msg_sender AS last_sender_wxid, last_sender_display_name,
		last_msg_type, last_msg_sub_type
		FROM SessionTable
		WHERE %s
		ORDER BY sort_timestamp DESC, username DESC
		LIMIT ? OFFSET ?`, strings.Join(where, " AND "))
	var warnings []string
	rows, err := collectSessionPage(getInt(a, "limit", 50), getInt(a, "offset", 0), typeFilter, func(fetchLimit, scanOffset int) ([]wcdb.Row, error) {
		queryArgs := append(append([]any(nil), args...), fetchLimit, scanOffset)
		batch, err := db.Query(query, queryArgs...)
		if err != nil {
			return nil, err
		}
		warnings = appendUniqueStrings(warnings, s.enrichSessionContacts(batch)...)
		return batch, nil
	})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		bk, _ := r["last_msg_type"].(int64)
		st, _ := r["last_msg_sub_type"].(int64)
		r["last_msg_kind_name"] = wxkind.Resolve(int32(bk), int32(st))
		// Aggregator sessions (brandsessionholder / brandservicesessionholder)
		// wrap the real sender in "_$_CUSTOM_USERNAME_PREFIX_$_<aggId>:<realId>".
		// The aggId is UI-internal noise; keep only the real wxid / gh_ id.
		if v, ok := r["last_sender_wxid"].(string); ok {
			r["last_sender_wxid"] = stripAggSenderPrefix(v)
		}
		for _, k := range []string{"last_sender_wxid", "last_sender_display_name"} {
			if v, ok := r[k].(string); ok && v == "" {
				delete(r, k)
			}
		}
	}
	return sessionRowsResult(rows, "live_session_db", warnings), nil
}

func (s *server) enrichSessionContacts(rows []wcdb.Row) []string {
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		username := rowString(row, "username")
		row["contact_type"] = wxkind.ClassifyUsername(username)
		row["display_name"] = username
	}
	db, err := s.openDB("contact", "contact.db")
	if err != nil {
		return []string{"session_contact_enrichment_unavailable"}
	}
	defer db.Close()
	// Keep each bound query below SQLite's portable parameter limit.
	for start := 0; start < len(rows); start += 500 {
		batch := rows[start:minInt(start+500, len(rows))]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		byUsername := make(map[string]wcdb.Row, len(batch))
		for i, row := range batch {
			username := rowString(row, "username")
			placeholders[i], args[i], byUsername[username] = "?", username, row
		}
		contacts, err := db.Query(`SELECT username, COALESCE(NULLIF(remark, ''), NULLIF(nick_name, ''), username) AS display_name, verify_flag FROM contact WHERE username IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return []string{"session_contact_enrichment_unavailable"}
		}
		for _, contact := range contacts {
			if row := byUsername[rowString(contact, "username")]; row != nil {
				row["display_name"] = contact["display_name"]
				row["is_verified"] = rowInt64(contact, "verify_flag")
			}
		}
	}
	return nil
}

func sessionRowsResult(rows []wcdb.Row, source string, warnings []string) cliRowsResult {
	status := "ready"
	if len(warnings) > 0 {
		status = "degraded"
	}
	return cliRowsResult{
		Rows: rows,
		Freshness: compactMap(map[string]any{
			"message_source":        source,
			"metadata_cache_status": status,
			"metadata_cache_role":   "not used for session ordering or unread counts",
		}),
		Warnings: warnings,
	}
}

func (s *server) toolUnread(a map[string]any) (any, error) {
	args := copyToolArgs(a)
	args["unread_only"] = true
	if getStr(args, "type_filter") == "" {
		args["type_filter"] = getStr(args, "filter")
	}
	return s.toolSessions(args)
}
