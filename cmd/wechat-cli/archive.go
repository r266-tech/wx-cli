package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/r266-tech/wx-cli/v2/internal/config"
	"github.com/r266-tech/wx-cli/v2/internal/diagnostics"
	"github.com/r266-tech/wx-cli/v2/internal/filelock"
	"github.com/r266-tech/wx-cli/v2/internal/safefile"
)

const digestSourceSchemaVersion = 1

func (s *server) toolArchiveCreate(a map[string]any) (any, error) {
	if strictReadOnlyMode() {
		return nil, fmt.Errorf("strict_read_only: archive create writes plaintext local snapshots")
	}
	if err := s.ensure(); err != nil {
		return nil, err
	}
	paths, err := s.cachePaths()
	if err != nil {
		return nil, err
	}
	root := strings.TrimSpace(getStr(a, "output"))
	if root == "" {
		state, err := appStateDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(state, "archives", time.Now().Format("20060102-150405.000000000"))
	}
	root, err = filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, err
	}
	if err := requirePrivateOutput(root, "archives", getBool(a, "confirm_external_output")); err != nil {
		return nil, err
	}
	lockPath, err := archiveMutationLockPath(root)
	if err != nil {
		return nil, err
	}
	unlock, err := filelock.Acquire(lockPath)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("archive output must be a real directory")
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return nil, fmt.Errorf("archive output directory is not empty; choose a new snapshot directory")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	sources, err := listSourceDBs(s.cfg, paths)
	if err != nil {
		return nil, err
	}
	records := make([]map[string]any, 0, len(sources))
	for _, src := range sources {
		rel := filepath.FromSlash(src.RelPath)
		dst := filepath.Join(root, rel)
		db, err := s.openDBNoRefresh(src.Subdir, src.File)
		record := map[string]any{"path": src.RelPath, "source_mtime": src.DBMTime}
		if err != nil {
			record["status"] = "error"
			record["error"] = redactArchiveError(err.Error())
			records = append(records, record)
			continue
		}
		err = db.BackupTo(dst)
		db.Close()
		if err != nil {
			record["status"] = "error"
			record["error"] = redactArchiveError(err.Error())
		} else {
			record["status"] = "ok"
			record["bytes"] = fileSize(dst)
			record["sha256"] = fileSHA256(dst)
		}
		records = append(records, record)
	}
	manifest := map[string]any{
		"schema_version": 1,
		"created_at":     time.Now().Format(time.RFC3339),
		"source":         "wechat-cli",
		"mode":           "explicit_plaintext_archive",
		"archive_root":   root,
		"account": map[string]any{
			"wxid":      "redacted",
			"db_root":   "redacted",
			"key_epoch": s.cfg.KeyEpoch,
		},
		"records": records,
		"privacy": map[string]any{
			"contains_plaintext_wechat_data": true,
			"retention_is_user_managed":      true,
			"do_not_sync_or_share":           true,
		},
	}
	manifestPath := filepath.Join(root, "archive-manifest.json")
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := atomicPrivateWrite(manifestPath, append(b, '\n')); err != nil {
		return nil, err
	}
	okCount := 0
	for _, record := range records {
		if record["status"] == "ok" {
			okCount++
		}
	}
	return map[string]any{
		"archive_root":   root,
		"manifest":       manifestPath,
		"database_count": len(records),
		"succeeded":      okCount,
		"failed":         len(records) - okCount,
		"privacy":        manifest["privacy"],
	}, nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func redactArchiveError(value string) string {
	return diagnostics.Redact(value)
}

func (s *server) toolDigestSource(a map[string]any) (any, error) {
	if strictReadOnlyMode() {
		return nil, fmt.Errorf("strict_read_only: digest-source writes local source files")
	}
	chat := strings.TrimSpace(getStr(a, "chat"))
	talker := strings.TrimSpace(getStr(a, "talker"))
	if chat == "" && talker == "" {
		return nil, fmt.Errorf("chat or talker is required")
	}
	if err := s.ensure(); err != nil {
		return nil, err
	}
	limit := getInt(a, "limit", 5000)
	if limit <= 0 {
		limit = 5000
	}
	if limit > 5000 {
		limit = 5000
	}

	args := scopedDigestArgs(a, s.cfg)
	resolvedTalker, err := s.resolveLooseChatArg(args)
	if err != nil {
		return nil, err
	}
	args["talker"] = resolvedTalker
	args["chat"] = ""
	args["view"] = "agent"
	args["limit"] = int64(limit)
	args["display_order"] = "asc"
	folder, err := digestStateFolder(args)
	if err != nil {
		return nil, err
	}
	stateName := digestStateName(args)
	unlock, err := filelock.Acquire(digestLockPath(folder))
	if err != nil {
		return nil, err
	}
	defer unlock()
	if since := getBool(a, "since_last"); since {
		args["order"] = "asc"
		previous, stateErr := loadDigestState(args)
		if stateErr != nil && !os.IsNotExist(stateErr) {
			return nil, fmt.Errorf("digest state invalid")
		}
		if stateErr == nil {
			// Offset only selects the initial window. Reapplying it after an
			// explicit saved cursor would skip another offset worth of messages.
			args["offset"] = 0
			if previous.MessageToken != "" {
				for _, key := range []string{"after", "after_message", "after_local_id", "after_server_id_str", "since", "since_time"} {
					delete(args, key)
				}
				args["after_message_token"] = previous.MessageToken
			} else if previous.LocalID > 0 {
				args["after_message"] = previous.LocalID
				if previous.ServerID != "" {
					args["after_server_id_str"] = previous.ServerID
				}
			} else if previous.Timestamp > 0 {
				args["after"] = fmt.Sprint(previous.Timestamp)
			}
		}
	}
	result, err := s.toolChatTimeline(args)
	if err != nil {
		return nil, err
	}
	payload, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("timeline returned unexpected payload")
	}
	warnings, _ := payload["warnings"].([]string)
	if err := requireCompleteMessageSource(warnings); err != nil {
		return nil, err
	}
	messages, _ := payload["messages"].([]map[string]any)
	if messages == nil {
		if raw, ok := payload["messages"].([]any); ok {
			messages = make([]map[string]any, 0, len(raw))
			for _, item := range raw {
				if row, ok := item.(map[string]any); ok {
					messages = append(messages, row)
				}
			}
		}
	}

	if messages == nil {
		messages = []map[string]any{}
	}
	name := firstNonEmpty(getStr(args, "chat"), getStr(args, "talker"), "chat")
	if query, ok := payload["query"].(map[string]any); ok {
		name = firstNonEmpty(getStr(query, "display_name"), getStr(query, "talker"), name)
	}
	if err := os.MkdirAll(filepath.Join(folder, "sources"), 0o700); err != nil {
		return nil, err
	}
	stamp := time.Now().Format("20060102-150405.000000000")
	jsonPath := filepath.Join(folder, "sources", stamp+".json")
	mdPath := filepath.Join(folder, "sources", stamp+".md")
	stats := digestStats(messages)
	archive := map[string]any{
		"schema_version": digestSourceSchemaVersion,
		"generated_at":   time.Now().Format(time.RFC3339),
		"source":         "wechat-cli",
		"account":        args["_digest_account"],
		"chat": map[string]any{
			"query":        name,
			"talker":       firstNonEmpty(getStr(args, "talker"), getStr(payload, "talker")),
			"display_name": name,
		},
		"range": map[string]any{
			"after":      args["after"],
			"before":     args["before"],
			"since_last": getBool(a, "since_last"),
		},
		"query":     payload["query"],
		"freshness": payload["freshness"],
		"warnings":  payload["warnings"],
		"stats":     stats,
		"messages":  messages,
	}
	jsonBytes, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := atomicPrivateWrite(jsonPath, append(jsonBytes, '\n')); err != nil {
		return nil, err
	}
	if err := atomicPrivateWrite(mdPath, []byte(renderDigestMarkdown(name, stats, messages))); err != nil {
		return nil, err
	}
	if latest := digestTimelineCursor(payload, messages); latest.MessageToken != "" || latest.Timestamp > 0 || latest.LocalID > 0 {
		if err := saveDigestStateNamed(folder, stateName, latest); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"schema_version":  digestSourceSchemaVersion,
		"folder":          folder,
		"checkpoint":      filepath.Join(folder, stateName),
		"source_json":     jsonPath,
		"source_markdown": mdPath,
		"message_count":   len(messages),
		"stats":           stats,
		"freshness":       payload["freshness"],
		"warnings":        payload["warnings"],
		"next_action":     "Use source_json or source_markdown for analysis; rerun with since_last=true for the next window.",
	}, nil
}

func digestRoot(a map[string]any) (string, error) {
	if value := strings.TrimSpace(getStr(a, "data_root")); value != "" {
		path, err := filepath.Abs(filepath.Clean(value))
		if err != nil {
			return "", err
		}
		if err := requirePrivateOutput(path, "digests", getBool(a, "confirm_external_output")); err != nil {
			return "", err
		}
		return path, nil
	}
	state, err := appStateDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(state, "digests")
	if err := requirePrivateOutput(path, "digests", false); err != nil {
		return "", err
	}
	return path, nil
}

func requirePrivateOutput(path, managedName string, confirmed bool) error {
	state, err := appStateDir()
	if err != nil {
		return err
	}
	if confirmed {
		return nil
	}
	resolvedState, err := resolveOutputPath(state)
	if err != nil {
		return err
	}
	managed := filepath.Join(resolvedState, managedName)
	clean, err := resolveOutputPath(path)
	if err != nil {
		return err
	}
	if pathWithin(managed, clean) {
		return nil
	}
	return fmt.Errorf("output is outside managed private %s; pass confirm_external_output=true", managedName)
}

func resolveOutputPath(path string) (string, error) {
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		// A dangling symlink is not a missing path that may be created safely.
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("output contains a dangling symbolic link")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}

func safeArchiveName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "chat"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r >= 0x80 {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "._")
	if name == "" {
		name = "chat"
	}
	if len(name) > 120 {
		end := 120
		for !utf8.RuneStart(name[end]) {
			end--
		}
		name = name[:end]
	}
	return name
}

func atomicPrivateWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".digest-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return safefile.Replace(tmpPath, path)
}

type digestCursor struct {
	MessageToken string `json:"last_message_token,omitempty"`
	Timestamp    int64  `json:"last_timestamp"`
	LocalID      int64  `json:"last_local_id,omitempty"`
	ServerID     string `json:"last_server_id,omitempty"`
}

func loadDigestState(a map[string]any) (digestCursor, error) {
	folder, err := digestStateFolder(a)
	if err != nil {
		return digestCursor{}, err
	}
	b, err := os.ReadFile(filepath.Join(folder, digestStateName(a)))
	if err != nil {
		return digestCursor{}, err
	}
	var state digestCursor
	if err := json.Unmarshal(b, &state); err != nil {
		return digestCursor{}, err
	}
	return state, nil
}

func saveDigestState(folder string, cursor digestCursor) error {
	return saveDigestStateNamed(folder, "state.json", cursor)
}

func saveDigestStateNamed(folder, name string, cursor digestCursor) error {
	b, err := json.MarshalIndent(map[string]any{
		"last_message_token": cursor.MessageToken,
		"schema_version":     1,
		"last_timestamp":     cursor.Timestamp,
		"last_local_id":      cursor.LocalID,
		"last_server_id":     cursor.ServerID,
		"updated_at":         time.Now().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	return atomicPrivateWrite(filepath.Join(folder, name), append(b, '\n'))
}

func scopedDigestArgs(a map[string]any, cfg *config.Config) map[string]any {
	args := copyToolArgs(a)
	account := sha256.Sum256([]byte(cfg.Wxid + "\x00" + filepath.Clean(cfg.DBRoot)))
	args["_digest_account"] = fmt.Sprintf("%x", account[:12])
	return args
}

func digestStateFolder(a map[string]any) (string, error) {
	root, err := digestRoot(a)
	if err != nil {
		return "", err
	}
	identity := firstNonEmpty(getStr(a, "talker"), getStr(a, "chat"), "chat")
	if account := getStr(a, "_digest_account"); account != "" {
		identity = account + "\x00" + identity
	}
	return filepath.Join(root, digestFolderName(identity)), nil
}

func digestStateName(a map[string]any) string {
	filters := map[string]any{}
	for _, key := range []string{"after", "before", "since_time", "since", "keyword", "type", "kind_name", "base_kind", "sender", "from_me", "offset", "before_message", "before_local_id", "before_server_id_str", "before_message_token", "after_message", "after_local_id", "after_server_id_str", "after_message_token"} {
		if value, exists := a[key]; exists && value != nil && fmt.Sprint(value) != "" {
			filters[key] = value
		}
	}
	if len(filters) == 0 {
		return "state.json"
	}
	b, _ := json.Marshal(filters)
	sum := sha256.Sum256(b)
	return fmt.Sprintf("state-%x.json", sum[:12])
}

func digestLockPath(folder string) string {
	return filepath.Join(filepath.Dir(folder), ".locks", filepath.Base(folder)+".lock")
}

func latestMessageTimestamp(messages []map[string]any) int64 {
	var latest int64
	for _, msg := range messages {
		for _, key := range []string{"create_time", "timestamp", "time"} {
			if value, ok := msg[key]; ok {
				if n, ok := value.(int64); ok && n > latest {
					latest = n
				}
				if n, ok := value.(float64); ok && int64(n) > latest {
					latest = int64(n)
				}
			}
		}
	}
	return latest
}

func latestMessageCursor(messages []map[string]any) digestCursor {
	var latest digestCursor
	for _, msg := range messages {
		timestamp := latestMessageTimestamp([]map[string]any{msg})
		localID := int64Value(msg["id"])
		if id, ok := msg["local_id"]; ok {
			localID = maxInt64(localID, int64Value(id))
		}
		if idMap, ok := msg["id"].(map[string]any); ok {
			localID = maxInt64(localID, int64Value(idMap["local_id"]))
		}
		if timestamp > latest.Timestamp || (timestamp == latest.Timestamp && localID > latest.LocalID) {
			latest = digestCursor{Timestamp: timestamp, LocalID: localID}
			if idMap, ok := msg["id"].(map[string]any); ok {
				latest.ServerID = getStr(idMap, "server_id_str")
				latest.MessageToken = getStr(idMap, "message_token")
			}
		}
	}
	return latest
}

func digestTimelineCursor(payload map[string]any, messages []map[string]any) digestCursor {
	// Timeline orders by sort_seq, which can differ from message timestamps.
	// Its cursor is the only safe checkpoint for the next after_message query.
	query, _ := payload["query"].(map[string]any)
	cursor, _ := query["cursor"].(map[string]any)
	localID := int64Value(cursor["next_after_message"])
	serverID := getStr(cursor, "next_after_server_id_str")
	if token := getStr(cursor, "next_after_message_token"); token != "" {
		for _, message := range messages {
			candidate := latestMessageCursor([]map[string]any{message})
			if candidate.MessageToken == token {
				return candidate
			}
		}
		return digestCursor{MessageToken: token, LocalID: localID, ServerID: serverID}
	}
	if localID > 0 {
		for _, message := range messages {
			candidate := latestMessageCursor([]map[string]any{message})
			if candidate.LocalID == localID && (serverID == "" || candidate.ServerID == serverID) {
				return candidate
			}
		}
		return digestCursor{LocalID: localID, ServerID: serverID}
	}
	return latestMessageCursor(messages)
}

func int64Value(value any) int64 {
	switch n := value.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func digestStats(messages []map[string]any) map[string]any {
	byKind := map[string]int{}
	bySender := map[string]int{}
	for _, msg := range messages {
		kind := firstNonEmpty(getStr(msg, "kind"), getStr(msg, "kind_name"), "unknown")
		sender := firstNonEmpty(getStr(msg, "sender"), getStr(msg, "sender_wxid"), "unknown")
		byKind[kind]++
		bySender[sender]++
	}
	return map[string]any{
		"message_count":  len(messages),
		"by_kind":        byKind,
		"by_sender":      sortedCounts(bySender),
		"last_timestamp": latestMessageTimestamp(messages),
	}
}

func sortedCounts(values map[string]int) []map[string]any {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if values[keys[i]] == values[keys[j]] {
			return keys[i] < keys[j]
		}
		return values[keys[i]] > values[keys[j]]
	})
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{"name": key, "count": values[key]})
	}
	return out
}

func renderDigestMarkdown(name string, stats map[string]any, messages []map[string]any) string {
	var b strings.Builder
	b.WriteString("# 微信聊天素材\n\n")
	b.WriteString("- 会话: ")
	b.WriteString(name)
	b.WriteString("\n- 消息数量: ")
	b.WriteString(fmt.Sprint(stats["message_count"]))
	b.WriteString("\n\n## 消息\n\n")
	for _, msg := range messages {
		timeText := firstNonEmpty(getStr(msg, "time_iso"), getStr(msg, "time"), "")
		sender := firstNonEmpty(getStr(msg, "sender"), getStr(msg, "sender_wxid"), "unknown")
		kind := firstNonEmpty(getStr(msg, "kind"), getStr(msg, "kind_name"), "unknown")
		text := firstNonEmpty(getStr(msg, "text"), getStr(msg, "display"), getStr(msg, "content_summary"), "")
		b.WriteString("- ")
		b.WriteString(timeText)
		b.WriteString(" ")
		b.WriteString(sender)
		b.WriteString(" [")
		b.WriteString(kind)
		b.WriteString("] ")
		b.WriteString(strings.ReplaceAll(text, "\n", " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func (s *server) toolArchiveValidate(a map[string]any) (any, error) {
	root := strings.TrimSpace(getStr(a, "input"))
	if root == "" {
		return nil, fmt.Errorf("input is required")
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(root, "archive-manifest.json")
	info, err := os.Stat(manifestPath)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("archive-manifest.json not found")
	}
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		SchemaVersion int              `json:"schema_version"`
		Records       []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		return nil, fmt.Errorf("invalid archive manifest: %w", err)
	}
	results := make([]map[string]any, 0, len(manifest.Records))
	valid := manifest.SchemaVersion == 1 && len(manifest.Records) > 0
	seen := make(map[string]bool, len(manifest.Records))
	for _, record := range manifest.Records {
		rel := strings.TrimSpace(fmt.Sprint(record["path"]))
		if record["status"] != "ok" {
			results = append(results, map[string]any{"path": rel, "status": "error", "error": "source database was not archived successfully"})
			valid = false
			continue
		}
		if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
			results = append(results, map[string]any{"path": rel, "status": "error", "error": "path escapes archive root"})
			valid = false
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		if seen[path] {
			results = append(results, map[string]any{"path": rel, "status": "error", "error": "duplicate archive path"})
			valid = false
			continue
		}
		seen[path] = true
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !pathWithin(root, resolved) {
			results = append(results, map[string]any{"path": rel, "status": "error", "error": "file is missing or escapes archive root"})
			valid = false
			continue
		}
		want := strings.TrimSpace(fmt.Sprint(record["sha256"]))
		got := fileSHA256(path)
		match := len(want) == 64 && got != "" && strings.EqualFold(want, got)
		item := map[string]any{"path": rel, "sha256": got, "status": "ok"}
		if !match {
			item["status"] = "error"
			item["expected_sha256"] = want
			valid = false
		}
		results = append(results, item)
	}
	return map[string]any{
		"valid":          valid,
		"trust":          "untrusted_archive_data",
		"archive_root":   root,
		"manifest":       manifestPath,
		"schema_version": manifest.SchemaVersion,
		"files":          results,
		"database_count": len(results),
	}, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func archiveBaseDir() (string, error) {
	state, err := appStateDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(state, "archives")
	if err := requirePrivateOutput(root, "archives", false); err != nil {
		return "", err
	}
	return root, nil
}

func archiveMutationLockPath(root string) (string, error) {
	resolved, err := resolveOutputPath(root)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		resolved = strings.ToLower(resolved)
	}
	state, err := appStateDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(resolved))
	return filepath.Join(state, ".archive-locks", fmt.Sprintf("%x.lock", sum[:])), nil
}

func (s *server) toolArchiveList(a map[string]any) (any, error) {
	root, err := archiveBaseDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"archives": []any{}, "quarantine": []any{}}, nil
		}
		return nil, err
	}
	archives := []map[string]any{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		manifest := filepath.Join(root, entry.Name(), "archive-manifest.json")
		if _, err := os.Stat(manifest); err == nil {
			archives = append(archives, map[string]any{"name": entry.Name(), "path": filepath.Join(root, entry.Name()), "manifest": manifest})
		}
	}
	quarantineRoot := filepath.Join(root, ".quarantine")
	quarantine := []map[string]any{}
	if items, err := os.ReadDir(quarantineRoot); err == nil {
		for _, entry := range items {
			if entry.IsDir() {
				quarantine = append(quarantine, map[string]any{"name": entry.Name(), "path": filepath.Join(quarantineRoot, entry.Name())})
			}
		}
	}
	return map[string]any{"archives": archives, "quarantine": quarantine}, nil
}

func (s *server) toolArchiveDelete(a map[string]any) (any, error) {
	if strictReadOnlyMode() {
		return nil, fmt.Errorf("strict_read_only_blocked")
	}
	if !getBool(a, "quarantine") {
		return nil, fmt.Errorf("archive delete requires quarantine=true")
	}
	name := strings.TrimSpace(getStr(a, "name"))
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("name must be a single archive directory name")
	}
	root, err := archiveBaseDir()
	if err != nil {
		return nil, err
	}
	src := filepath.Join(root, name)
	unlock, err := lockManagedArchiveMutation(src, "archive")
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(src, "archive-manifest.json")); err != nil {
		return nil, fmt.Errorf("archive not found: %s", name)
	}
	dst, err := quarantineManagedFolder(root, src, "archive")
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "quarantined", "from": src, "to": dst, "receipt": time.Now().Format(time.RFC3339)}, nil
}

func (s *server) toolArchiveRestore(a map[string]any) (any, error) {
	if strictReadOnlyMode() {
		return nil, fmt.Errorf("strict_read_only_blocked")
	}
	name := strings.TrimSpace(getStr(a, "name"))
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("name must be a single quarantine directory name")
	}
	root, err := archiveBaseDir()
	if err != nil {
		return nil, err
	}
	src := filepath.Join(root, ".quarantine", name)
	if err := requirePrivateOutput(src, "archives", false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(src)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("quarantined archive not found: %s", name)
	}
	origin, err := loadQuarantineOrigin(src)
	if err != nil {
		return nil, err
	}
	destinationRoot := root
	if origin.Kind == "digest" {
		destinationRoot, err = digestRoot(nil)
		if err != nil {
			return nil, err
		}
	}
	dst := filepath.Join(destinationRoot, origin.Name)
	unlock, err := lockManagedArchiveMutation(dst, origin.Kind)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := os.Lstat(dst); err == nil {
		return nil, fmt.Errorf("archive destination already exists: %s", filepath.Base(dst))
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(destinationRoot, 0o700); err != nil {
		return nil, err
	}
	if err := os.Rename(src, dst); err != nil {
		return nil, err
	}
	_ = os.Remove(src + ".origin.json")
	return map[string]any{"status": "restored", "kind": origin.Kind, "from": src, "to": dst, "receipt": time.Now().Format(time.RFC3339)}, nil
}

type quarantineOrigin struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Name          string `json:"original_name"`
}

func lockManagedArchiveMutation(path, kind string) (func(), error) {
	if kind == "digest" {
		return filelock.Acquire(digestLockPath(path))
	}
	lockPath, err := archiveMutationLockPath(path)
	if err != nil {
		return nil, err
	}
	return filelock.Acquire(lockPath)
}

func quarantineManagedFolder(archiveRoot, src, kind string) (string, error) {
	if err := requirePrivateOutput(src, kind+"s", false); err != nil {
		return "", err
	}
	info, err := os.Lstat(src)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("quarantine source must be a real directory")
	}
	root := filepath.Join(archiveRoot, ".quarantine")
	if err := requirePrivateOutput(root, "archives", false); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	origin := quarantineOrigin{SchemaVersion: 1, Kind: kind, Name: filepath.Base(src)}
	dst := filepath.Join(root, time.Now().Format("20060102-150405.000000000")+"-"+kind+"-"+safeArchiveName(origin.Name))
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		return "", fmt.Errorf("quarantine destination already exists or is inaccessible")
	}
	b, err := json.Marshal(origin)
	if err != nil {
		return "", err
	}
	if err := atomicPrivateWrite(dst+".origin.json", append(b, '\n')); err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Remove(dst + ".origin.json")
		return "", err
	}
	return dst, nil
}

func loadQuarantineOrigin(src string) (quarantineOrigin, error) {
	var origin quarantineOrigin
	b, err := os.ReadFile(src + ".origin.json")
	if err == nil {
		if json.Unmarshal(b, &origin) != nil || origin.SchemaVersion != 1 {
			return origin, fmt.Errorf("invalid quarantine origin receipt")
		}
	} else if os.IsNotExist(err) {
		// Before origin receipts existed, quarantine names had a full date and
		// time prefix. Removing only the first '-' loses the original name.
		name := filepath.Base(src)
		if len(name) <= 16 || name[15] != '-' {
			return origin, fmt.Errorf("quarantine origin receipt is missing")
		}
		if _, err := time.Parse("20060102-150405", name[:15]); err != nil {
			return origin, fmt.Errorf("quarantine origin receipt is missing")
		}
		origin = quarantineOrigin{SchemaVersion: 1, Kind: "archive", Name: name[16:]}
		if _, err := os.Stat(filepath.Join(src, "archive-manifest.json")); os.IsNotExist(err) {
			if _, err := os.Stat(filepath.Join(src, "state.json")); err != nil {
				return origin, fmt.Errorf("legacy quarantine kind could not be identified")
			}
			origin.Kind = "digest"
		}
	} else {
		return origin, err
	}
	if (origin.Kind != "archive" && origin.Kind != "digest") || origin.Name == "" || strings.HasPrefix(origin.Name, ".") || filepath.Base(origin.Name) != origin.Name || strings.ContainsAny(origin.Name, "/\\") {
		return origin, fmt.Errorf("invalid quarantine origin receipt")
	}
	return origin, nil
}

// toolArchiveRetention applies the bounded, recoverable retention policy. It never
// scans outside the managed state root and permanently deletes nothing.
func (s *server) toolArchiveRetention(a map[string]any) (any, error) {
	archiveRoot, err := archiveBaseDir()
	if err != nil {
		return nil, err
	}
	digestRoot, err := digestRoot(nil)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	quarantineRoot := filepath.Join(archiveRoot, ".quarantine")
	if err := requirePrivateOutput(quarantineRoot, "archives", false); err != nil {
		return nil, err
	}
	apply := getBool(a, "apply")
	if apply && strictReadOnlyMode() {
		return nil, fmt.Errorf("strict_read_only: retention changes local files")
	}
	type candidate struct {
		Path, Kind string
		AgeDays    int
	}
	var candidates []candidate
	collect := func(root, kind string, maxAge time.Duration) error {
		entries, e := os.ReadDir(root)
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			info, e := entry.Info()
			if e != nil {
				continue
			}
			age := now.Sub(info.ModTime())
			if age > maxAge {
				candidates = append(candidates, candidate{filepath.Join(root, entry.Name()), kind, int(age.Hours() / 24)})
			}
		}
		return nil
	}
	if err := collect(archiveRoot, "archive", 14*24*time.Hour); err != nil {
		return nil, err
	}
	if err := collect(digestRoot, "digest", 14*24*time.Hour); err != nil {
		return nil, err
	}
	if apply {
		if err := os.MkdirAll(quarantineRoot, 0o700); err != nil {
			return nil, err
		}
	}
	moved := []map[string]any{}
	if apply {
		for _, c := range candidates {
			unlock, err := lockManagedArchiveMutation(c.Path, c.Kind)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(c.Path)
			if err != nil || now.Sub(info.ModTime()) <= 14*24*time.Hour {
				unlock()
				if err != nil {
					return nil, err
				}
				continue
			}
			dst, err := quarantineManagedFolder(archiveRoot, c.Path, c.Kind)
			unlock()
			if err != nil {
				return nil, err
			}
			moved = append(moved, map[string]any{"kind": c.Kind, "from": c.Path, "to": dst, "age_days": c.AgeDays})
		}
	}
	return map[string]any{"policy": map[string]any{"digest_days": 14, "archive_days": 14, "quarantine_days": 7}, "dry_run": !apply, "candidates": candidates, "moved": moved, "permanent_delete": false, "receipt_at": now.Format(time.RFC3339)}, nil
}

func digestFolderName(talker string) string {
	sum := sha256.Sum256([]byte(talker))
	return fmt.Sprintf("chat-%x", sum[:12])
}
