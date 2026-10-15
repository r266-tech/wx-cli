package main

import (
	"fmt"
	"strconv"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
	"github.com/r266-tech/wx-cli/v2/internal/wxkind"
)

type contextAnchorRef struct {
	LocalID  int64
	ServerID int64
	Kind     string
	Token    string
}

func (s *server) toolMessageContext(a map[string]any) (any, error) {
	talker, err := s.resolveLooseChatArg(a)
	if err != nil {
		return nil, err
	}
	if talker == "" {
		return nil, fmt.Errorf("talker or chat is required")
	}
	if aggregatorSessions[talker] {
		return nil, fmt.Errorf("%q is an aggregator session; pass the concrete gh_*/wxid talker instead", talker)
	}
	ref, err := contextAnchorRefFromArgs(a)
	if err != nil {
		return nil, err
	}
	if ref.Token != "" {
		if _, err := s.validateMessageTokenScope(ref.Token, talker); err != nil {
			return nil, err
		}
	}
	beforeCount := contextCountArg(a, "before_count", "before_messages", 20)
	afterCount := contextCountArg(a, "after_count", "after_messages", 20)
	includeAnchor := getBoolDefault(a, "include_anchor", true)

	tableName := "Msg_" + talkerHash(talker)
	shards, err := s.findMsgDBs(tableName)
	if err != nil {
		if ref.Token != "" {
			return nil, fmt.Errorf("message_cursor_stale: original message source is unavailable: %w", err)
		}
		return nil, err
	}
	defer closeMsgDBs(shards)
	warnings := msgShardWarnings(shards)
	mediaEnrichmentFailed := false

	anchor, err := s.findContextAnchorRow(shards, tableName, ref)
	if err != nil {
		return nil, err
	}
	older, err := s.contextWindowRows(shards, tableName, anchor, "before", beforeCount)
	if err != nil {
		return nil, err
	}
	newer, err := s.contextWindowRows(shards, tableName, anchor, "after", afterCount)
	if err != nil {
		return nil, err
	}
	warnings = appendUniqueStrings(warnings, msgShardWarnings(shards)...)

	sortLiveMessageRows(older, "sort_seq ASC, local_id ASC, server_id ASC")
	sortLiveMessageRows(newer, "sort_seq ASC, local_id ASC, server_id ASC")
	rows := make([]wcdb.Row, 0, len(older)+len(newer)+1)
	rows = append(rows, older...)
	if includeAnchor {
		anchor["context_role"] = "anchor"
		rows = append(rows, anchor)
	}
	rows = append(rows, newer...)
	for _, r := range rows {
		if rowInt64(r, "local_id") == rowInt64(anchor, "local_id") && rowInt64(r, "server_id") == rowInt64(anchor, "server_id") && rowString(r, "_message_shard") == rowString(anchor, "_message_shard") {
			r["context_role"] = "anchor"
		} else if contextRowBeforeAnchor(r, anchor) {
			r["context_role"] = "before"
		} else {
			r["context_role"] = "after"
		}
	}
	if getStr(a, "display_order") == "desc" {
		sortLiveMessageRows(rows, "sort_seq DESC, local_id DESC, server_id DESC")
	}
	allRows := append([]wcdb.Row{anchor}, rows...)
	setContextTalker(talker, allRows)
	for _, row := range allRows {
		s.attachMessageToken(talker, rowString(row, "_message_shard"), row)
	}
	if includeMediaPathsForMessages(a) {
		if err := s.enrichMessageMediaResources(rows); err != nil {
			mediaEnrichmentFailed = true
			warnings = appendUniqueStrings(warnings, "media_enrichment_failed: "+err.Error())
		}
	}
	s.finishContextRows(talker, allRows)
	messages := agentMessages(rows, includeDebugOutput(a))
	attachContextRoles(messages, rows)
	return map[string]any{
		"query": compactMap(map[string]any{
			"chat":           getStr(a, "chat"),
			"talker":         talker,
			"display_name":   rowString(anchor, "talker_display_name"),
			"anchor":         agentMessageID(anchor),
			"before_count":   beforeCount,
			"after_count":    afterCount,
			"include_anchor": includeAnchor,
			"display_order":  firstNonEmpty(getStr(a, "display_order"), "asc"),
			"returned":       len(messages),
		}),
		"freshness": compactMap(map[string]any{
			"message_source":            "live_message_db",
			"metadata_cache_role":       "chat/sender display names only",
			"anchor_time":               agentMessageTime(anchor),
			"anchor_time_iso":           agentMessageTimeISO(anchor),
			"media_enrichment_complete": !mediaEnrichmentFailed,
		}),
		"warnings": warnings,
		"messages": messages,
	}, nil
}

func contextAnchorRefFromArgs(a map[string]any) (contextAnchorRef, error) {
	ref, ok, err := contextAnchorRefFromKeys(a,
		[]string{"local_id", "message_local_id", "around_local_id"},
		[]string{"server_id_str", "message_server_id_str", "around_server_id_str", "server_id", "message_server_id", "around_server_id"}, "message_token")
	if ok || err != nil {
		return ref, err
	}
	return contextAnchorRef{}, fmt.Errorf("message_token, local_id or server_id is required")
}

func contextAnchorRefFromKeys(a map[string]any, localKeys, serverKeys []string, tokenKeys ...string) (contextAnchorRef, bool, error) {
	var ref contextAnchorRef
	found := false
	for _, key := range localKeys {
		if n, ok, err := int64Arg(a, key); ok || err != nil {
			if err == nil && n <= 0 {
				err = fmt.Errorf("invalid argument %q: expected positive message id, got %d", key, n)
			}
			if err != nil {
				return ref, ok, err
			}
			ref.LocalID, ref.Kind, found = n, key, true
			break
		}
	}
	for _, key := range serverKeys {
		if n, ok, err := int64Arg(a, key); ok || err != nil {
			if err == nil && n <= 0 {
				err = fmt.Errorf("invalid argument %q: expected positive message id, got %d", key, n)
			}
			if err != nil {
				return ref, ok, err
			}
			ref.ServerID, found = n, true
			if ref.Kind == "" {
				ref.Kind = key
			}
			break
		}
	}
	for _, key := range tokenKeys {
		if raw := getStr(a, key); raw != "" {
			if _, err := parseMessageToken(raw); err != nil {
				return ref, true, err
			}
			ref.Token, ref.Kind, found = raw, key, true
			break
		}
	}
	return ref, found, nil
}

func contextCountArg(a map[string]any, primary, alias string, def int) int {
	n := getInt(a, primary, -1)
	if n < 0 {
		n = getInt(a, alias, -1)
	}
	if n < 0 {
		n = getInt(a, "limit", def)
	}
	if n < 0 {
		n = def
	}
	if n > 500 {
		return 500
	}
	return n
}

func messageBoundaryRefFromArgs(a map[string]any, direction string) (contextAnchorRef, bool, error) {
	switch direction {
	case "before":
		return contextAnchorRefFromKeys(a,
			[]string{"before_message", "before_message_local_id", "before_local_id", "before_message_id"},
			[]string{"before_server_id_str", "before_message_server_id_str", "before_server_id", "before_message_server_id"}, "before_message_token")
	case "after":
		return contextAnchorRefFromKeys(a,
			[]string{"after_message", "after_message_local_id", "after_local_id", "after_message_id", "since_local_id", "since_message"},
			[]string{"after_server_id_str", "after_message_server_id_str", "after_server_id", "after_message_server_id"}, "after_message_token")
	default:
		return contextAnchorRef{}, false, fmt.Errorf("invalid message boundary direction %q", direction)
	}
}

func messageBoundaryQueryValue(a map[string]any, direction string) string {
	ref, ok, err := messageBoundaryRefFromArgs(a, direction)
	if !ok || err != nil {
		return ""
	}
	if ref.Token != "" {
		return ref.Token
	}
	if ref.LocalID > 0 {
		return fmt.Sprintf("local_id:%d", ref.LocalID)
	}
	if ref.ServerID > 0 {
		return fmt.Sprintf("server_id:%d", ref.ServerID)
	}
	return ""
}

func messageBoundaryWhere(anchor wcdb.Row, direction string, shardOpt ...string) (string, []any, error) {
	sortSeq := rowInt64(anchor, "sort_seq")
	localID := rowInt64(anchor, "local_id")
	serverID := rowInt64(anchor, "server_id")
	op := "<"
	if direction == "after" {
		op = ">"
	} else if direction != "before" {
		return "", nil, fmt.Errorf("invalid message boundary direction %q", direction)
	}
	args := []any{sortSeq, sortSeq, localID, localID, serverID}
	serverWhere := "COALESCE(server_id, 0) " + op + " ?"
	if len(shardOpt) > 0 && rowString(anchor, "_message_shard") != "" {
		serverWhere = "(" + serverWhere + " OR (COALESCE(server_id, 0) = ? AND ? " + op + " ?))"
		args = append(args, serverID, shardOpt[0], rowString(anchor, "_message_shard"))
	}
	return "(sort_seq " + op + " ? OR (sort_seq = ? AND (local_id " + op + " ? OR (local_id = ? AND " + serverWhere + "))))", args, nil
}

func (s *server) findContextAnchorRow(shards []msgShardDB, tableName string, ref contextAnchorRef) (wcdb.Row, error) {
	if ref.Token != "" {
		return s.findMessageTokenAnchor(shards, tableName, ref)
	}
	var where string
	var args []any
	if ref.LocalID != 0 {
		where = "local_id = ?"
		args = append(args, ref.LocalID)
		if ref.ServerID != 0 {
			where += " AND server_id = ?"
			args = append(args, ref.ServerID)
		}
	} else {
		where = "server_id = ?"
		args = append(args, ref.ServerID)
	}
	rows, err := s.queryContextRows(shards, tableName, where, args, "sort_seq DESC, local_id DESC, server_id DESC", 10)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("anchor message not found for %s", ref.Kind)
	}
	sortLiveMessageRows(rows, "sort_seq DESC, local_id DESC, server_id DESC")
	for _, row := range rows[1:] {
		if rowInt64(row, "local_id") != rowInt64(rows[0], "local_id") ||
			rowInt64(row, "server_id") != rowInt64(rows[0], "server_id") ||
			rowInt64(row, "sort_seq") != rowInt64(rows[0], "sort_seq") ||
			rowInt64(row, "create_time") != rowInt64(rows[0], "create_time") ||
			rowString(row, "_message_shard") != rowString(rows[0], "_message_shard") {
			return nil, fmt.Errorf("anchor message is ambiguous across message shards for %s; pass its message_token or an unambiguous server_id_str together with local_id", ref.Kind)
		}
	}
	return rows[0], nil
}

func (s *server) contextWindowRows(shards []msgShardDB, tableName string, anchor wcdb.Row, direction string, limit int) ([]wcdb.Row, error) {
	if limit <= 0 {
		return nil, nil
	}
	var order string
	switch direction {
	case "before":
		order = "sort_seq DESC, local_id DESC, server_id DESC"
	case "after":
		order = "sort_seq ASC, local_id ASC, server_id ASC"
	default:
		return nil, fmt.Errorf("invalid context direction %q", direction)
	}
	var rows []wcdb.Row
	for i, shard := range shards {
		where, args, err := messageBoundaryWhere(anchor, direction, shard.Name)
		if err != nil {
			return nil, err
		}
		batch, err := s.queryContextRows(shards[i:i+1], tableName, where, args, order, limit)
		if err != nil {
			return nil, err
		}
		rows = append(rows, batch...)
	}
	sortLiveMessageRows(rows, order)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (s *server) queryContextRows(shards []msgShardDB, tableName, where string, args []any, order string, limit int) ([]wcdb.Row, error) {
	var rows []wcdb.Row
	shardLimit := limit
	if shardLimit <= 0 {
		shardLimit = 1
	}
	for i, shard := range shards {
		qargs := append([]any(nil), args...)
		qargs = append(qargs, shardLimit)
		shardRows, err := shard.DB.Query(fmt.Sprintf(`SELECT local_id, server_id, local_type, sort_seq,
			real_sender_id, create_time, status, message_content, source
			FROM %s WHERE %s
			ORDER BY %s LIMIT ?`, quoteIdent(tableName), where, order), qargs...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", shard.Name, err)
		}
		n2i, nameErr := loadName2Id(shard.DB)
		if nameErr != nil {
			shards[i].Warnings = appendUniqueStrings(shards[i].Warnings, "sender_metadata_unavailable: "+shard.Name)
		}
		if n2i != nil {
			shardRows = resolveSenders(shardRows, n2i)
		}
		for _, row := range shardRows {
			row["_message_shard"] = shard.Name
		}
		rows = append(rows, shardRows...)
	}
	return prepareContextRows(rows), nil
}

func prepareContextRows(rows []wcdb.Row) []wcdb.Row {
	rows = enrichMessages(decodeFields(rows, "message_content", "source"))
	return rows
}

func (s *server) finishContextRows(talker string, rows []wcdb.Row) {
	if len(rows) == 0 {
		return
	}
	setContextTalker(talker, rows)
	s.attachMessageDisplayNames(rows)
	if selfWxid := s.selfWxid(); selfWxid != "" {
		for _, r := range rows {
			sw := rowString(r, "sender_wxid")
			if sw != "" {
				r["is_from_me"] = (sw == selfWxid)
			}
		}
	}
	for _, r := range rows {
		if sid := rowInt64(r, "server_id"); sid != 0 {
			r["server_id_str"] = strconv.FormatInt(sid, 10)
		}
		delete(r, "real_sender_id")
		delete(r, "status")
		delete(r, "source")
		delete(r, "local_type")
		delete(r, "sort_seq")
	}
}

func setContextTalker(talker string, rows []wcdb.Row) {
	for _, r := range rows {
		if rowString(r, "talker") == "" && talker != "" {
			r["talker"] = talker
		}
		if rowString(r, "chat_type") == "" && talker != "" {
			r["chat_type"] = agentChatType(talker, wxkind.ClassifyUsername(talker), false)
		}
	}
}

func contextRowBeforeAnchor(row, anchor wcdb.Row) bool {
	return messageRowLess(row, anchor)
}

func attachContextRoles(messages []map[string]any, rows []wcdb.Row) {
	// agentMessages preserves row order; local IDs can be reused across shards.
	for i, msg := range messages {
		if i < len(rows) {
			if role := rowString(rows[i], "context_role"); role != "" {
				msg["context_role"] = role
			}
		}
	}
}
