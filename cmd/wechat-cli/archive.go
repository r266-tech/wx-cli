package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
		root = filepath.Join(state, "archives", time.Now().Format("20060102-150405"))
	}
	root, err = filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, err
	}
	if err := requirePrivateOutput(root, "archives", getBool(a, "confirm_external_output")); err != nil {
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
	buf := make([]byte, 128*1024)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func redactArchiveError(value string) string {
	for _, token := range []string{"salt ", "enc_key ", "wxid_"} {
		if i := strings.Index(value, token); i >= 0 {
			return value[:i] + "[redacted]"
		}
	}
	return value
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
	limit := getInt(a, "limit", 5000)
	if limit <= 0 {
		limit = 5000
	}
	if limit > 5000 {
		limit = 5000
	}

	args := copyToolArgs(a)
	args["view"] = "agent"
	args["limit"] = int64(limit)
	args["display_order"] = "asc"
	if talker != "" {
		args["talker"] = talker
	}
	if since := getBool(a, "since_last"); since {
		if previous, err := loadDigestState(args); err == nil {
			if previous.LocalID > 0 {
				args["after_message"] = previous.LocalID
			} else if previous.Timestamp > 0 {
				args["after"] = previous.Timestamp
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

	name := firstNonEmpty(getStr(args, "chat"), getStr(args, "talker"), "chat")
	if query, ok := payload["query"].(map[string]any); ok {
		name = firstNonEmpty(getStr(query, "display_name"), getStr(query, "talker"), name)
	}
	root, err := digestRoot(a)
	if err != nil {
		return nil, err
	}
	folder := filepath.Join(root, safeArchiveName(name))
	if err := os.MkdirAll(filepath.Join(folder, "sources"), 0o700); err != nil {
		return nil, err
	}
	stamp := time.Now().Format("20060102-150405")
	jsonPath := filepath.Join(folder, "sources", stamp+".json")
	mdPath := filepath.Join(folder, "sources", stamp+".md")
	stats := digestStats(messages)
	archive := map[string]any{
		"schema_version": digestSourceSchemaVersion,
		"generated_at":   time.Now().Format(time.RFC3339),
		"source":         "wechat-cli",
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
	if latest := latestMessageCursor(messages); latest.Timestamp > 0 || latest.LocalID > 0 {
		if err := saveDigestState(folder, latest); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"schema_version":  digestSourceSchemaVersion,
		"folder":          folder,
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
	return filepath.Join(state, "digests"), nil
}

func requirePrivateOutput(path, managedName string, confirmed bool) error {
	state, err := appStateDir()
	if err != nil {
		return err
	}
	managed, err := filepath.Abs(filepath.Join(state, managedName))
	if err != nil {
		return err
	}
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	if confirmed || pathWithin(managed, clean) {
		return nil
	}
	return fmt.Errorf("output is outside managed private %s; pass confirm_external_output=true", managedName)
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
		name = name[:120]
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
	return os.Rename(tmpPath, path)
}

type digestCursor struct {
	Timestamp int64  `json:"last_timestamp"`
	LocalID   int64  `json:"last_local_id,omitempty"`
	ServerID  string `json:"last_server_id,omitempty"`
}

func loadDigestState(a map[string]any) (digestCursor, error) {
	root, err := digestRoot(a)
	if err != nil {
		return digestCursor{}, err
	}
	name := safeArchiveName(firstNonEmpty(getStr(a, "chat"), getStr(a, "talker"), "chat"))
	b, err := os.ReadFile(filepath.Join(root, name, "state.json"))
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
	b, err := json.MarshalIndent(map[string]any{
		"schema_version": 1,
		"last_timestamp": cursor.Timestamp,
		"last_local_id":  cursor.LocalID,
		"last_server_id": cursor.ServerID,
		"updated_at":     time.Now().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	return atomicPrivateWrite(filepath.Join(folder, "state.json"), append(b, '\n'))
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
			latest.Timestamp = timestamp
			latest.LocalID = localID
			if idMap, ok := msg["id"].(map[string]any); ok {
				latest.ServerID = getStr(idMap, "server_id_str")
			}
		}
	}
	return latest
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
	valid := manifest.SchemaVersion == 1
	for _, record := range manifest.Records {
		rel := strings.TrimSpace(fmt.Sprint(record["path"]))
		if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
			results = append(results, map[string]any{"path": rel, "status": "error", "error": "path escapes archive root"})
			valid = false
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !pathWithin(root, resolved) {
			results = append(results, map[string]any{"path": rel, "status": "error", "error": "file is missing or escapes archive root"})
			valid = false
			continue
		}
		want := strings.TrimSpace(fmt.Sprint(record["sha256"]))
		got := fileSHA256(path)
		match := want != "" && strings.EqualFold(want, got)
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
	return filepath.Join(state, "archives"), nil
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
	if !getBool(a, "quarantine") {
		return nil, fmt.Errorf("archive delete requires quarantine=true")
	}
	name := strings.TrimSpace(getStr(a, "name"))
	if name == "" || filepath.Base(name) != name || strings.Contains(name, string(os.PathSeparator)) {
		return nil, fmt.Errorf("name must be a single archive directory name")
	}
	root, err := archiveBaseDir()
	if err != nil {
		return nil, err
	}
	src := filepath.Join(root, name)
	if _, err := os.Stat(filepath.Join(src, "archive-manifest.json")); err != nil {
		return nil, fmt.Errorf("archive not found: %s", name)
	}
	quarantineRoot := filepath.Join(root, ".quarantine")
	if err := os.MkdirAll(quarantineRoot, 0o700); err != nil {
		return nil, err
	}
	dst := filepath.Join(quarantineRoot, time.Now().Format("20060102-150405")+"-"+safeArchiveName(name))
	if err := os.Rename(src, dst); err != nil {
		return nil, err
	}
	return map[string]any{"status": "quarantined", "from": src, "to": dst, "receipt": time.Now().Format(time.RFC3339)}, nil
}

func (s *server) toolArchiveRestore(a map[string]any) (any, error) {
	name := strings.TrimSpace(getStr(a, "name"))
	if name == "" || filepath.Base(name) != name {
		return nil, fmt.Errorf("name must be a single quarantine directory name")
	}
	root, err := archiveBaseDir()
	if err != nil {
		return nil, err
	}
	src := filepath.Join(root, ".quarantine", name)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("quarantined archive not found: %s", name)
	}
	dstName := name
	if i := strings.Index(dstName, "-"); i >= 0 {
		dstName = dstName[i+1:]
	}
	dst := filepath.Join(root, safeArchiveName(dstName))
	if _, err := os.Stat(dst); err == nil {
		return nil, fmt.Errorf("archive destination already exists: %s", filepath.Base(dst))
	}
	if err := os.Rename(src, dst); err != nil {
		return nil, err
	}
	return map[string]any{"status": "restored", "from": src, "to": dst, "receipt": time.Now().Format(time.RFC3339)}, nil
}

// toolArchiveRetention applies the bounded, recoverable retention policy. It never
// scans outside the managed state root and permanently deletes nothing.
func (s *server) toolArchiveRetention(a map[string]any) (any, error) {
	state, err := appStateDir()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	archiveRoot := filepath.Join(state, "archives")
	digestRoot := filepath.Join(state, "digests")
	quarantineRoot := filepath.Join(archiveRoot, ".quarantine")
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
	if err := os.MkdirAll(quarantineRoot, 0o700); err != nil && apply {
		return nil, err
	}
	moved := []map[string]any{}
	if apply {
		for _, c := range candidates {
			dst := filepath.Join(quarantineRoot, now.Format("20060102-150405")+"-"+safeArchiveName(filepath.Base(c.Path)))
			if err := os.Rename(c.Path, dst); err != nil {
				return nil, err
			}
			moved = append(moved, map[string]any{"kind": c.Kind, "from": c.Path, "to": dst, "age_days": c.AgeDays})
		}
	}
	return map[string]any{"policy": map[string]any{"digest_days": 14, "archive_days": 14, "quarantine_days": 7}, "dry_run": !apply, "candidates": candidates, "moved": moved, "permanent_delete": false, "receipt_at": now.Format(time.RFC3339)}, nil
}
