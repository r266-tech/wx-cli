package main

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

func (s *server) toolReadEvents(a map[string]any) (any, error) {
	mode := strings.TrimSpace(strings.ToLower(getStr(a, "mode")))
	if mode == "" || mode == "auto" {
		if firstNonEmpty(getStr(a, "chat"), getStr(a, "talker")) != "" {
			mode = "messages"
		} else {
			mode = "sessions"
		}
	}
	switch mode {
	case "messages":
		return s.readMessageEvents(a)
	case "sessions":
		return s.readSessionEvents(a)
	default:
		return nil, fmt.Errorf("invalid mode=%q: must be messages or sessions", mode)
	}
}

func (s *server) readMessageEvents(a map[string]any) (any, error) {
	args, catchUp, err := readMessageEventArgs(a)
	if err != nil {
		return nil, err
	}

	raw, err := s.toolChatTimeline(args)
	if err != nil {
		return nil, err
	}
	env, _ := raw.(map[string]any)
	if err := requireCompleteMessageSource(stringSliceAny(env["warnings"])); err != nil {
		return nil, fmt.Errorf("message_cursor_source_incomplete: %w", err)
	}
	messages := mapSliceAny(env["messages"])
	events := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		event := compactMap(map[string]any{
			"type":       "message",
			"event_time": msg["time_iso"],
			"message":    msg,
		})
		if id := mapStringAny(msg["id"]); len(id) > 0 {
			event["cursor"] = readEventsCursorFromMessageID(id)
		}
		events = append(events, event)
	}
	timelineQuery := mapStringAny(env["query"])
	hasMore := catchUp && getBoolDefault(timelineQuery, "has_more", false)
	nextCursor := newestReadEventsCursor(events)
	if nextCursor == "" {
		nextCursor = currentMessageReadEventsCursor(args)
	}
	out := compactMap(map[string]any{
		"query": compactMap(map[string]any{
			"mode":     "messages",
			"chat":     firstNonEmpty(getStr(a, "chat"), getStr(a, "talker")),
			"from_me":  queryFromMeArg(a),
			"limit":    getInt(args, "limit", 50),
			"cursor":   getStr(a, "cursor"),
			"returned": len(events),
			"has_more": hasMore,
		}),
		"freshness": env["freshness"],
		"warnings":  env["warnings"],
		"cursor":    nextCursor,
	})
	out["events"] = events
	return out, nil
}

func readMessageEventArgs(a map[string]any) (map[string]any, bool, error) {
	args := copyToolArgs(a)
	if args["limit"] == nil {
		args["limit"] = int64(50)
	}
	if cursor := getStr(args, "cursor"); cursor != "" {
		// since_* establishes the first poll only. --follow reuses the same
		// flags with each returned cursor, so those initial bounds must not
		// override the cursor or replay the first batch indefinitely.
		cursorArgs := map[string]any{}
		if strings.HasPrefix(cursor, "msg:") {
			if _, err := parseMessageToken(cursor); err != nil {
				return nil, false, err
			}
		}
		applyReadEventsCursor(cursorArgs, cursor)
		if cursorArgs["after_message"] == nil && getStr(cursorArgs, "after_message_token") == "" && getStr(cursorArgs, "after_server_id_str") == "" && getStr(cursorArgs, "after") == "" {
			return nil, false, fmt.Errorf("invalid message cursor")
		}
		for _, key := range []string{"since_local_id", "since_time", "since", "after", "after_message", "after_local_id", "after_message_token", "after_server_id_str"} {
			delete(args, key)
		}
		for key, value := range cursorArgs {
			args[key] = value
		}
	} else {
		if v := firstNonEmpty(getStr(args, "since_time"), getStr(args, "since")); v != "" && args["after"] == nil {
			args["after"] = v
		}
		if id, ok, err := int64Arg(args, "since_local_id"); err != nil {
			return nil, false, err
		} else if ok && args["after_message"] == nil {
			args["after_message"] = id
		}
	}
	catchUp := args["after_message"] != nil || getStr(args, "after_message_token") != "" || getStr(args, "after_server_id_str") != "" || getStr(args, "after") != ""
	if catchUp {
		// Catch-up reads must take the oldest unseen rows first. Taking a DESC
		// page and advancing to its newest row permanently skips the middle of a
		// backlog when more than limit messages arrived between polls.
		args["order"] = "asc"
		args["display_order"] = "query"
	} else {
		// With no starting cursor, preserve tail's snapshot behavior: return the
		// latest N messages displayed chronologically.
		args["order"] = "desc"
		args["display_order"] = "asc"
	}
	return args, catchUp, nil
}

func (s *server) readSessionEvents(a map[string]any) (any, error) {
	storage, err := sessionCursorStorage(a)
	if err != nil {
		return nil, err
	}
	args, err := sessionTailArgs(a)
	if err != nil {
		return nil, err
	}
	limit := getInt(a, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	scanLimit := getInt(a, "scan_limit", 5000)
	if scanLimit <= 0 {
		scanLimit = 5000
	}
	if scanLimit < limit {
		scanLimit = limit
	}
	if scanLimit > sessionCursorMaxStates || limit > sessionCursorMaxStates {
		return nil, fmt.Errorf("session limit and scan_limit must not exceed %d", sessionCursorMaxStates)
	}
	if err := s.ensure(); err != nil {
		return nil, err
	}
	binding := sessionCursorBinding(s.cfg, args)
	inputCursor := strings.TrimSpace(getStr(a, "cursor"))
	var previous *sessionStateCursor
	var legacy *sessionReadEventsCursor
	if isSessionStateCursor(inputCursor) {
		cursor, err := readSessionStateCursor(inputCursor, binding)
		if err != nil {
			return nil, err
		}
		previous = &cursor
	} else {
		cursor, catchUp, err := readEventsSessionCursor(a)
		if err != nil {
			return nil, err
		}
		if catchUp {
			legacy = &cursor
		}
	}
	args["limit"] = int64(scanLimit + 1)
	raw, err := s.toolSessions(args)
	if err != nil {
		return nil, err
	}
	rows, ok := cliResultRows(raw)
	if !ok {
		return nil, fmt.Errorf("read_events internal error: sessions returned %T", raw)
	}
	freshness, warnings := cliRowsResultMetadata(raw)
	if freshness == nil {
		freshness = map[string]any{"message_source": "live_session_db"}
	}
	scanTruncated := len(rows) > scanLimit
	blocked := scanTruncated || len(warnings) > 0 || getStr(freshness, "metadata_cache_status") == "degraded"
	if scanTruncated {
		warnings = appendUniqueStrings(warnings, "session_scan_truncated")
	}
	if blocked {
		warnings = appendUniqueStrings(warnings, "session_cursor_not_advanced")
	}
	if legacy != nil && !blocked {
		warnings = appendUniqueStrings(warnings, "session_cursor_upgraded", "unread_changes_before_upgrade_unavailable")
	}
	events := []map[string]any{}
	nextCursor := inputCursor
	hasMore := blocked
	if !blocked {
		batch, err := buildSessionStateBatch(rows, binding, previous, legacy, limit)
		if err != nil {
			return nil, err
		}
		events, hasMore = batch.Events, batch.HasMore
		prefix := ""
		position := len(batch.Snapshot.Updates)
		if previous != nil && len(events) == 0 && previous.Storage == storage {
			prefix, position = previous.Prefix, previous.Applied
		} else {
			prefix, err = writeSessionSnapshot(batch.Snapshot, storage)
			if err != nil {
				return nil, err
			}
		}
		nextCursor = sessionCursorAt(prefix, position)
		for i, event := range events {
			if storage == "local" {
				event["cursor"] = sessionCursorAt(prefix, i+1)
			} else {
				event["cursor_position"] = i + 1
			}
		}
	}
	freshness["observed_changes"] = "session_state_diff"
	freshness["cursor_precision"] = "session_state_fingerprint"
	freshness["unread_updates_tracked"] = true
	freshness["change_semantics"] = "net_changes_between_polls"
	freshness["cursor_scope"] = "event"
	if storage == "inline" {
		freshness["cursor_scope"] = "batch"
	}
	query := map[string]any{
		"mode": "sessions", "limit": limit, "scan_limit": scanLimit,
		"cursor_provided": inputCursor != "", "cursor_storage": storage,
		"returned": len(events), "has_more": hasMore, "scan_truncated": scanTruncated,
		"initial_snapshot": previous == nil && legacy == nil,
		"type_filter":      getStr(args, "type_filter"), "unread_only": getBool(args, "unread_only"),
	}
	if keyword := getStr(args, "keyword"); keyword != "" {
		query["keyword"] = keyword
	}
	out := compactMap(map[string]any{"query": query, "freshness": freshness, "warnings": warnings})
	out["cursor"], out["events"] = nextCursor, events
	return out, nil
}

type sessionReadEventsCursor struct {
	Timestamp int64
	Username  string
}

type sessionEventCandidate struct {
	Timestamp int64
	Username  string
	Row       map[string]any
}

func buildSessionEventBatch(rows []map[string]any, cursor sessionReadEventsCursor, catchUp bool, limit int) ([]map[string]any, bool) {
	candidates := make([]sessionEventCandidate, 0, len(rows))
	for _, row := range rows {
		candidate := sessionEventCandidate{
			Timestamp: firstNonZeroInt64(int64MapValue(row, "last_timestamp"), int64MapValue(row, "sort_timestamp")),
			Username:  stringMapValue(row, "username"),
			Row:       row,
		}
		if candidate.Timestamp <= 0 {
			continue
		}
		if catchUp && !sessionEventCandidateAfter(candidate, cursor) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Timestamp != candidates[j].Timestamp {
			return candidates[i].Timestamp < candidates[j].Timestamp
		}
		return candidates[i].Username < candidates[j].Username
	})
	hasMore := false
	if catchUp {
		if len(candidates) > limit {
			hasMore = true
			candidates = candidates[:limit]
		}
	} else if len(candidates) > limit {
		// An initial tail call establishes a cursor at the latest snapshot; older
		// sessions are history, not an incremental backlog for that cursor.
		candidates = candidates[len(candidates)-limit:]
	}
	events := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		events = append(events, compactMap(map[string]any{
			"type":       "session",
			"event_time": cliFormatUnixISO(candidate.Timestamp),
			"cursor": encodeSessionReadEventsCursor(sessionReadEventsCursor{
				Timestamp: candidate.Timestamp,
				Username:  candidate.Username,
			}),
			"session": candidate.Row,
		}))
	}
	return events, hasMore
}

func sessionEventCandidateAfter(candidate sessionEventCandidate, cursor sessionReadEventsCursor) bool {
	if candidate.Timestamp != cursor.Timestamp {
		return candidate.Timestamp > cursor.Timestamp
	}
	if cursor.Username == "" {
		// Legacy timestamp-only cursors mean the whole timestamp was consumed.
		return false
	}
	return candidate.Username > cursor.Username
}

func applyReadEventsCursor(args map[string]any, cursor string) {
	cursor = strings.TrimSpace(cursor)
	switch {
	case strings.HasPrefix(cursor, messageTokenPrefix):
		if _, err := parseMessageToken(cursor); err == nil {
			args["after_message_token"] = cursor
		}
	case strings.HasPrefix(cursor, "server_id:"):
		if n, err := strconv.ParseInt(strings.TrimPrefix(cursor, "server_id:"), 10, 64); err == nil && n > 0 {
			args["after_server_id_str"] = strconv.FormatInt(n, 10)
		}
	case strings.HasPrefix(cursor, "local_id:"):
		if n, err := strconv.ParseInt(strings.TrimPrefix(cursor, "local_id:"), 10, 64); err == nil && n > 0 {
			args["after_message"] = n
		}
	case strings.HasPrefix(cursor, "message:"):
		if n, err := strconv.ParseInt(strings.TrimPrefix(cursor, "message:"), 10, 64); err == nil && n > 0 {
			args["after_message"] = n
		}
	case strings.HasPrefix(cursor, "time:"):
		args["after"] = strings.TrimPrefix(cursor, "time:")
	}
}

func readEventsCursorFromMessageID(id map[string]any) string {
	if token := getStr(id, "message_token"); token != "" {
		return token
	}
	if n, ok, err := int64Arg(id, "server_id_str"); err == nil && ok && n > 0 {
		return fmt.Sprintf("server_id:%d", n)
	}
	if n := int64MapValue(id, "local_id"); n > 0 {
		return fmt.Sprintf("local_id:%d", n)
	}
	return ""
}

func currentMessageReadEventsCursor(args map[string]any) string {
	if cursor := getStr(args, "cursor"); cursor != "" {
		return cursor
	}
	if token := getStr(args, "after_message_token"); token != "" {
		return token
	}
	if id, ok, err := int64Arg(args, "after_server_id_str"); err == nil && ok && id > 0 {
		return fmt.Sprintf("server_id:%d", id)
	}
	if id, ok, _ := int64Arg(args, "after_message"); ok && id > 0 {
		return fmt.Sprintf("local_id:%d", id)
	}
	if after := getStr(args, "after"); after != "" {
		return "time:" + after
	}
	return ""
}

func newestReadEventsCursor(events []map[string]any) string {
	for i := len(events) - 1; i >= 0; i-- {
		if cursor := rowString(wcdb.Row(events[i]), "cursor"); cursor != "" {
			return cursor
		}
	}
	return ""
}

func readEventsSessionCursor(a map[string]any) (sessionReadEventsCursor, bool, error) {
	if raw := getStr(a, "cursor"); raw != "" {
		cursor, err := parseSessionReadEventsCursor(raw)
		return cursor, true, err
	}
	if s := firstNonEmpty(getStr(a, "since_time"), getStr(a, "since"), getStr(a, "after")); s != "" {
		ts, err := parseTS(s)
		return sessionReadEventsCursor{Timestamp: ts}, true, err
	}
	return sessionReadEventsCursor{}, false, nil
}

func parseSessionReadEventsCursor(raw string) (sessionReadEventsCursor, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 4096 {
		return sessionReadEventsCursor{}, fmt.Errorf("legacy session cursor exceeds the supported size")
	}
	if !strings.HasPrefix(raw, "session:") {
		return sessionReadEventsCursor{}, fmt.Errorf("invalid session cursor %q", raw)
	}
	parts := strings.SplitN(strings.TrimPrefix(raw, "session:"), ":", 2)
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || ts < 0 {
		return sessionReadEventsCursor{}, fmt.Errorf("invalid session cursor %q", raw)
	}
	cursor := sessionReadEventsCursor{Timestamp: ts}
	if len(parts) == 1 || parts[1] == "" {
		return cursor, nil
	}
	username, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return sessionReadEventsCursor{}, fmt.Errorf("invalid session cursor %q", raw)
	}
	cursor.Username = string(username)
	return cursor, nil

}

func encodeSessionReadEventsCursor(cursor sessionReadEventsCursor) string {
	if cursor.Timestamp <= 0 {
		return ""
	}
	if cursor.Username == "" {
		return fmt.Sprintf("session:%d", cursor.Timestamp)
	}
	return fmt.Sprintf("session:%d:%s", cursor.Timestamp,
		base64.RawURLEncoding.EncodeToString([]byte(cursor.Username)))
}

func mapStringAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func readEventsPollInterval(a map[string]any) time.Duration {
	raw := firstNonEmpty(getStr(a, "poll_interval"), getStr(a, "interval"))
	if raw == "" {
		return 2 * time.Second
	}
	if n, ok, _ := int64Arg(a, "poll_interval"); ok && n > 0 {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 2 * time.Second
	}
	if d < 500*time.Millisecond {
		return 500 * time.Millisecond
	}
	return d
}
