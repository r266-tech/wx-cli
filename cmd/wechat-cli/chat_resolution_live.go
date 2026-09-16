package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
	"github.com/r266-tech/wx-cli/v2/internal/wxkind"
)

// Resolve names against the same live account as timeline and sessions. An old
// metadata snapshot must not hide a newly joined group or renamed contact.
func (s *server) liveChatCandidates(query, typeFilter string, limit int) ([]chatCandidate, []string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil, fmt.Errorf("query/chat is required")
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	db, err := s.openDBNoRefresh("contact", "contact.db")
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	like := "%" + strings.ReplaceAll(query, " ", "") + "%"
	rows, err := db.Query(`SELECT username, nick_name, remark, alias, verify_flag,
		COALESCE(NULLIF(remark, ''), NULLIF(nick_name, ''), username) AS display_name
		FROM contact WHERE username = ?
		OR REPLACE(username,' ','') LIKE ? COLLATE NOCASE
		OR REPLACE(nick_name,' ','') LIKE ? COLLATE NOCASE
		OR REPLACE(remark,' ','') LIKE ? COLLATE NOCASE
		OR REPLACE(alias,' ','') LIKE ? COLLATE NOCASE
		ORDER BY (username = ? OR nick_name = ? OR remark = ? OR alias = ?) DESC, username
		LIMIT 2001`, query, like, like, like, like, query, query, query, query)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	if len(rows) > 2000 {
		rows = rows[:2000]
		warnings = append(warnings, "name_resolution_candidates_truncated")
	}
	byUser := make(map[string]wcdb.Row, len(rows))
	for _, r := range rows {
		byUser[rowString(r, "username")] = r
	}
	if len(rows) > 0 {
		sessions, err := s.openDBNoRefresh("session", "session.db")
		if err != nil {
			warnings = append(warnings, "session_ordering_unavailable")
		} else {
			defer sessions.Close()
			for start := 0; start < len(rows); start += 500 {
				batch := rows[start:minInt(start+500, len(rows))]
				args := make([]any, len(batch))
				ph := make([]string, len(batch))
				for i, r := range batch {
					args[i], ph[i] = rowString(r, "username"), "?"
				}
				info, err := sessions.Query(`SELECT username,last_timestamp,sort_timestamp FROM SessionTable WHERE COALESCE(is_hidden,0)=0 AND username IN (`+strings.Join(ph, ",")+`)`, args...)
				if err != nil {
					warnings = appendUniqueStrings(warnings, "session_ordering_unavailable")
					break
				}
				for _, row := range info {
					if r := byUser[rowString(row, "username")]; r != nil {
						r["last_timestamp"], r["sort_timestamp"], r["source"] = row["last_timestamp"], row["sort_timestamp"], "session"
					}
				}
			}
		}
	}
	candidates := make([]chatCandidate, 0, len(rows))
	for _, r := range rows {
		u := rowString(r, "username")
		kind := agentChatType(u, wxkind.ClassifyUsername(u), rowInt64(r, "verify_flag") != 0)
		if !chatTypeAllowed(kind, typeFilter) {
			continue
		}
		candidates = append(candidates, chatCandidate{Username: u, DisplayName: rowString(r, "display_name"), ChatType: kind, LastTimestamp: rowInt64(r, "last_timestamp"), SortTimestamp: rowInt64(r, "sort_timestamp"), Source: firstNonEmpty(rowString(r, "source"), "contact"), Match: candidateMatch(query, r)})
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if rankCandidate(a) != rankCandidate(b) {
			return rankCandidate(a) > rankCandidate(b)
		}
		if a.SortTimestamp != b.SortTimestamp {
			return a.SortTimestamp > b.SortTimestamp
		}
		if a.DisplayName != b.DisplayName {
			return a.DisplayName < b.DisplayName
		}
		return a.Username < b.Username
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, warnings, nil
}

func (s *server) resolveLiveChat(query, typeFilter string) (string, error) {
	candidates, warnings, _, err := s.chatCandidates(query, typeFilter, 5)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("chat %q not found; call resolve-chat to inspect candidates", query)
	}
	if ambiguousChatCandidates(candidates) || (len(warnings) > 0 && !strings.Contains(candidates[0].Match, "_exact")) {
		return "", fmt.Errorf("chat %q is ambiguous; call resolve-chat and pass a returned username/talker", query)
	}
	return candidates[0].Username, nil
}

func (s *server) chatCandidates(query, typeFilter string, limit int) ([]chatCandidate, []string, string, error) {
	candidates, warnings, liveErr := s.liveChatCandidates(query, typeFilter, limit)
	if liveErr == nil {
		return candidates, warnings, "live_contact_db_plus_live_session_db", nil
	}
	// Preserve offline name lookup as a clearly labelled fallback. This never
	// supplies cached previews, unread counts, or message bodies.
	db, cacheWarnings, err := s.openCacheIndexWithWarnings()
	if err != nil {
		return nil, nil, "", liveErr
	}
	defer db.Close()
	candidates, err = resolveChatCandidates(db, query, typeFilter, limit)
	if err != nil {
		return nil, nil, "", liveErr
	}
	return candidates, appendUniqueStrings(cacheWarnings, "live_name_resolution_unavailable"), "metadata_cache_names", nil
}
