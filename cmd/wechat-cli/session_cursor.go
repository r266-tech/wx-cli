package main

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/r266-tech/wx-cli/v2/internal/config"
)

const (
	sessionCursorSchema       = 1
	sessionCursorMaxStates    = 100000
	sessionCursorMaxJSONBytes = 16 << 20
	sessionCursorMaxGzipBytes = 8 << 20
	sessionLocalCursorPrefix  = "session-local:v1:"
	sessionInlineCursorPrefix = "session-inline:v1:"
)

// A snapshot stores only usernames and fingerprints, excluding previews and
// other chat content. Applying an event prefix reconstructs exactly
// the baseline acknowledged by that event, leaving later changes unconsumed.
type sessionCursorSnapshot struct {
	Schema  int                           `json:"schema"`
	Binding string                        `json:"binding"`
	Base    map[string]sessionCursorState `json:"base"`
	Updates []sessionCursorUpdate         `json:"updates"`
}

type sessionCursorUpdate struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Hash     string `json:"hash,omitempty"`
	Removed  bool   `json:"removed,omitempty"`
}

type sessionCursorState struct {
	Username string `json:"username"`
	Hash     string `json:"hash"`
}

type sessionStateCursor struct {
	Snapshot sessionCursorSnapshot
	Applied  int
	Storage  string
	Prefix   string
}

func sessionCursorStorage(a map[string]any) (string, error) {
	storage := strings.ToLower(strings.TrimSpace(getStr(a, "cursor_storage")))
	if storage == "" || storage == "auto" {
		if strictReadOnlyMode() {
			return "inline", nil
		}
		return "local", nil
	}
	if storage != "local" && storage != "inline" {
		return "", fmt.Errorf("cursor_storage must be auto, local or inline")
	}
	if storage == "local" && strictReadOnlyMode() {
		return "", fmt.Errorf("strict_read_only: local session cursors write state; use cursor_storage=inline or auto")
	}
	return storage, nil
}

func sessionCursorBinding(cfg *config.Config, a map[string]any) string {
	scope := struct {
		Account, Root, Type, Keyword string
		UnreadOnly                   bool
	}{cfg.Wxid, filepath.Clean(cfg.DBRoot), getStr(a, "type_filter"), getStr(a, "keyword"), getBool(a, "unread_only")}
	b, _ := json.Marshal(scope)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func sessionIdentity(username string) string {
	h := sha256.Sum256([]byte(username))
	return hex.EncodeToString(h[:16])
}

func sessionFingerprint(row map[string]any) (string, error) {
	b, err := json.Marshal(row)
	if err != nil {
		return "", fmt.Errorf("session state cannot be fingerprinted: %w", err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func isSessionStateCursor(raw string) bool {
	return strings.HasPrefix(raw, sessionLocalCursorPrefix) || strings.HasPrefix(raw, sessionInlineCursorPrefix)
}

func readSessionStateCursor(raw, binding string) (sessionStateCursor, error) {
	var result sessionStateCursor
	storage, payload := "", ""
	switch {
	case strings.HasPrefix(raw, sessionLocalCursorPrefix):
		storage, payload = "local", strings.TrimPrefix(raw, sessionLocalCursorPrefix)
	case strings.HasPrefix(raw, sessionInlineCursorPrefix):
		storage, payload = "inline", strings.TrimPrefix(raw, sessionInlineCursorPrefix)
	default:
		return result, fmt.Errorf("invalid session state cursor")
	}
	maxEncoded := base64.RawURLEncoding.EncodedLen(sessionCursorMaxGzipBytes)
	if len(payload) > maxEncoded+32 {
		return result, fmt.Errorf("session cursor exceeds the supported size")
	}
	split := strings.LastIndexByte(payload, ':')
	if split <= 0 {
		return result, fmt.Errorf("invalid session state cursor position")
	}
	position := payload[split+1:]
	applied, err := strconv.Atoi(position)
	if err != nil || applied < 0 || strconv.Itoa(applied) != position {
		return result, fmt.Errorf("invalid session state cursor position")
	}
	payload = payload[:split]
	var compressed []byte
	if storage == "local" {
		if !sessionCursorHex(payload, 32) {
			return result, fmt.Errorf("invalid local session cursor identifier")
		}
		compressed, err = readLocalSessionSnapshot(payload)
	} else {
		compressed, err = base64.RawURLEncoding.Strict().DecodeString(payload)
	}
	if err != nil {
		return result, fmt.Errorf("session cursor cannot be loaded: %w", err)
	}
	snapshot, err := decodeSessionSnapshot(compressed)
	if err != nil {
		return result, err
	}
	if snapshot.Binding != binding {
		return result, fmt.Errorf("session cursor account or query scope does not match; start a new cursor for this scope")
	}
	if applied > len(snapshot.Updates) {
		return result, fmt.Errorf("session cursor position exceeds its event batch")
	}
	result = sessionStateCursor{Snapshot: snapshot, Applied: applied, Storage: storage, Prefix: raw[:strings.LastIndexByte(raw, ':')]}
	return result, nil
}

func (cursor sessionStateCursor) states() map[string]sessionCursorState {
	states := make(map[string]sessionCursorState, len(cursor.Snapshot.Base)+cursor.Applied)
	for id, state := range cursor.Snapshot.Base {
		states[id] = state
	}
	for _, update := range cursor.Snapshot.Updates[:cursor.Applied] {
		if update.Removed {
			delete(states, update.ID)
		} else {
			states[update.ID] = sessionCursorState{Username: update.Username, Hash: update.Hash}
		}
	}
	return states
}

func sessionCursorHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validateSessionSnapshot(snapshot sessionCursorSnapshot) error {
	if snapshot.Schema != sessionCursorSchema || !sessionCursorHex(snapshot.Binding, 64) || snapshot.Base == nil || snapshot.Updates == nil {
		return fmt.Errorf("invalid session cursor snapshot schema")
	}
	if len(snapshot.Base) > sessionCursorMaxStates || len(snapshot.Updates) > sessionCursorMaxStates {
		return fmt.Errorf("session cursor contains too many states")
	}
	for id, state := range snapshot.Base {
		if !validSessionIdentity(id, state.Username) || !sessionCursorHex(state.Hash, 64) {
			return fmt.Errorf("invalid session cursor state fingerprint")
		}
	}
	seen := make(map[string]bool, len(snapshot.Updates))
	for _, update := range snapshot.Updates {
		if seen[update.ID] || !validSessionIdentity(update.ID, update.Username) || (update.Removed && update.Hash != "") || (!update.Removed && !sessionCursorHex(update.Hash, 64)) {
			return fmt.Errorf("invalid session cursor update")
		}
		seen[update.ID] = true
	}
	return nil
}

func validSessionIdentity(id, username string) bool {
	return username != "" && len(username) <= 1024 && utf8.ValidString(username) && sessionCursorHex(id, 32) && id == sessionIdentity(username)
}

func encodeSessionSnapshot(snapshot sessionCursorSnapshot) ([]byte, error) {
	if err := validateSessionSnapshot(snapshot); err != nil {
		return nil, err
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if len(b) > sessionCursorMaxJSONBytes {
		return nil, fmt.Errorf("session cursor snapshot exceeds the supported size")
	}
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() > sessionCursorMaxGzipBytes {
		return nil, fmt.Errorf("compressed session cursor exceeds the supported size")
	}
	return compressed.Bytes(), nil
}

func decodeSessionSnapshot(compressed []byte) (sessionCursorSnapshot, error) {
	var snapshot sessionCursorSnapshot
	if len(compressed) > sessionCursorMaxGzipBytes {
		return snapshot, fmt.Errorf("compressed session cursor exceeds the supported size")
	}
	raw := bytes.NewReader(compressed)
	r, err := gzip.NewReader(raw)
	if err != nil {
		return snapshot, fmt.Errorf("invalid compressed session cursor")
	}
	r.Multistream(false)
	b, readErr := io.ReadAll(io.LimitReader(r, sessionCursorMaxJSONBytes+1))
	closeErr := r.Close()
	if readErr != nil || closeErr != nil || len(b) > sessionCursorMaxJSONBytes || raw.Len() != 0 {
		return snapshot, fmt.Errorf("invalid or oversized compressed session cursor")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, fmt.Errorf("invalid session cursor snapshot")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return snapshot, fmt.Errorf("session cursor snapshot has trailing data")
	}
	return snapshot, validateSessionSnapshot(snapshot)
}

func sessionSnapshotPath(id string) (string, error) {
	state, err := appStateDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(state, "session-cursors", id+".json.gz")
	if err := requirePrivateOutput(path, "session-cursors", false); err != nil {
		return "", err
	}
	return path, nil
}

func readLocalSessionSnapshot(id string) ([]byte, error) {
	path, err := sessionSnapshotPath(id)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("local session cursor is unavailable; retain the original state directory or start a new cursor")
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(path))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("local session cursor is missing or is not a regular file")
	}
	f, err := root.Open(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() > sessionCursorMaxGzipBytes {
		return nil, fmt.Errorf("local session cursor changed or exceeds the supported size")
	}
	b, err := io.ReadAll(io.LimitReader(f, sessionCursorMaxGzipBytes+1))
	if len(b) > sessionCursorMaxGzipBytes {
		return nil, fmt.Errorf("local session cursor exceeds the supported size")
	}
	return b, err
}

func writeSessionSnapshot(snapshot sessionCursorSnapshot, storage string) (string, error) {
	compressed, err := encodeSessionSnapshot(snapshot)
	if err != nil {
		return "", err
	}
	if storage == "inline" {
		return sessionInlineCursorPrefix + base64.RawURLEncoding.EncodeToString(compressed), nil
	}
	if storage != "local" || strictReadOnlyMode() {
		return "", fmt.Errorf("local session cursor storage is not allowed")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(random[:])
	path, err := sessionSnapshotPath(id)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return "", fmt.Errorf("session cursor identifier is already in use")
	}
	if err := atomicPrivateWrite(path, compressed); err != nil {
		return "", err
	}
	return sessionLocalCursorPrefix + id, nil
}

func sessionCursorAt(prefix string, applied int) string {
	return prefix + ":" + strconv.Itoa(applied)
}

func sessionTailArgs(a map[string]any) (map[string]any, error) {
	if getInt(a, "offset", 0) != 0 {
		return nil, fmt.Errorf("session tail does not support offset; resume using cursor")
	}
	args := copyToolArgs(a)
	filters := map[string]bool{}
	for _, part := range strings.Split(getStr(a, "type_filter"), ",") {
		filter := normalizeChatType(part)
		switch filter {
		case "all", "group", "private", "official_account", "bot", "folded", "corp_im", "stranger":
			filters[filter] = true
		default:
			return nil, fmt.Errorf("unsupported session type_filter %q", part)
		}
	}
	names := make([]string, 0, len(filters))
	for name := range filters {
		names = append(names, name)
	}
	sort.Strings(names)
	args["type_filter"] = strings.Join(names, ",")
	if filters["all"] {
		args["type_filter"] = "all"
	}
	args["offset"] = 0
	return args, nil
}

type sessionStateBatch struct {
	Snapshot sessionCursorSnapshot
	Events   []map[string]any
	HasMore  bool
}

// Build a diff from the acknowledged baseline, not from the last attempted
// query. Unreturned changes remain in the next diff, including when a consumer
// resumes from a local per-event cursor partway through a batch.
func buildSessionStateBatch(rows []map[string]any, binding string, previous *sessionStateCursor, legacy *sessionReadEventsCursor, limit int) (sessionStateBatch, error) {
	batch := sessionStateBatch{
		Snapshot: sessionCursorSnapshot{Schema: sessionCursorSchema, Binding: binding, Base: map[string]sessionCursorState{}, Updates: []sessionCursorUpdate{}},
		Events:   []map[string]any{},
	}
	current := make(map[string]sessionCursorState, len(rows))
	candidates := make([]sessionEventCandidate, 0, len(rows))
	for _, row := range rows {
		username := stringMapValue(row, "username")
		id := sessionIdentity(username)
		if !validSessionIdentity(id, username) {
			return batch, fmt.Errorf("session source contains an invalid username")
		}
		if _, exists := current[id]; exists {
			return batch, fmt.Errorf("session source contains a duplicate identity")
		}
		hash, err := sessionFingerprint(row)
		if err != nil {
			return batch, err
		}
		current[id] = sessionCursorState{Username: username, Hash: hash}
		candidates = append(candidates, sessionEventCandidate{
			Timestamp: firstNonZeroInt64(int64MapValue(row, "last_timestamp"), int64MapValue(row, "sort_timestamp")),
			Username:  username,
			Row:       row,
		})
	}
	if len(current) > sessionCursorMaxStates {
		return batch, fmt.Errorf("session source exceeds the supported state count")
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Timestamp != candidates[j].Timestamp {
			return candidates[i].Timestamp < candidates[j].Timestamp
		}
		return candidates[i].Username < candidates[j].Username
	})
	if previous != nil {
		batch.Snapshot.Base = previous.states()
	} else {
		for id, state := range current {
			batch.Snapshot.Base[id] = state
		}
		if legacy == nil {
			if len(candidates) > limit {
				candidates = candidates[len(candidates)-limit:]
			}
		} else {
			pending := candidates[:0]
			for _, candidate := range candidates {
				if sessionEventCandidateAfter(candidate, *legacy) {
					pending = append(pending, candidate)
				}
			}
			candidates = pending
		}
		// Initial snapshots acknowledge older rows without presenting them as a
		// backlog, but selected rows and legacy catch-up rows stay unacknowledged
		// until their corresponding event is consumed.
		for _, candidate := range candidates {
			delete(batch.Snapshot.Base, sessionIdentity(candidate.Username))
		}
	}
	now := time.Now().Format(time.RFC3339Nano)
	add := func(update sessionCursorUpdate, event map[string]any) {
		batch.Snapshot.Updates = append(batch.Snapshot.Updates, update)
		batch.Events = append(batch.Events, event)
	}
	removed := make([]string, 0)
	for id, state := range batch.Snapshot.Base {
		if _, exists := current[id]; !exists {
			removed = append(removed, state.Username)
		}
	}
	sort.Strings(removed)
	for _, username := range removed {
		id := sessionIdentity(username)
		add(sessionCursorUpdate{ID: id, Username: username, Removed: true}, map[string]any{
			"type": "session", "change": "removed", "session_id": id, "event_time": now,
			"session": map[string]any{"username": username, "removed": true},
		})
	}
	for _, candidate := range candidates {
		id := sessionIdentity(candidate.Username)
		state := current[id]
		before, exists := batch.Snapshot.Base[id]
		if exists && before.Hash == state.Hash {
			continue
		}
		change := "added"
		if exists {
			change = "updated"
		}
		add(sessionCursorUpdate{ID: id, Username: state.Username, Hash: state.Hash}, map[string]any{
			"type": "session", "change": change, "session_id": id, "event_time": now, "session": candidate.Row,
		})
	}
	if len(batch.Events) > limit {
		batch.HasMore = true
		batch.Events = batch.Events[:limit]
		batch.Snapshot.Updates = batch.Snapshot.Updates[:limit]
	}
	return batch, nil
}
