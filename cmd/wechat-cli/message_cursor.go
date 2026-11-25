package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
)

const messageTokenPrefix = "msg:v1:"
const messageTokenV2Prefix = "m2."
const maxMessageTokenLength = 4096

// v2 tokens are about a third of v1's size. Every message row carries one, so
// they were over a third of a timeline's bytes. Format:
// m2.<account 16 hex>.<m|b><shard number>.<local>.<server>.<sort>.<create>.<talker>
// with numbers in base 36; talker is last because it may contain dots.
// v1 tokens stay accepted, so saved cursors keep working.
var messageTokenShardRE = regexp.MustCompile(`^(message|biz_message)_(\d+)\.db$`)

func isMessageToken(raw string) bool {
	return strings.HasPrefix(raw, messageTokenPrefix) || strings.HasPrefix(raw, messageTokenV2Prefix)
}

// A token is a versioned locator, not an authorization credential. All fields
// are checked against the active account and an actual row in its original
// shard before the token can be used as a context or pagination anchor.
type messageToken struct {
	Account    string `json:"a"`
	Talker     string `json:"t"`
	Shard      string `json:"d"`
	LocalID    int64  `json:"l"`
	ServerID   int64  `json:"s"`
	SortSeq    int64  `json:"q"`
	CreateTime int64  `json:"c"`
}

func parseMessageToken(raw string) (messageToken, error) {
	var token messageToken
	if len(raw) > maxMessageTokenLength {
		return token, fmt.Errorf("message_cursor_invalid: unsupported or oversized message token")
	}
	accountBytes := 16
	switch {
	case strings.HasPrefix(raw, messageTokenV2Prefix):
		parsed, ok := parseMessageTokenV2(strings.TrimPrefix(raw, messageTokenV2Prefix))
		if !ok {
			return token, fmt.Errorf("message_cursor_invalid: malformed message token")
		}
		token, accountBytes = parsed, 8
	case strings.HasPrefix(raw, messageTokenPrefix):
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, messageTokenPrefix))
		if err != nil {
			return token, fmt.Errorf("message_cursor_invalid: malformed message token")
		}
		if err := json.Unmarshal(data, &token); err != nil {
			return token, fmt.Errorf("message_cursor_invalid: malformed message token payload")
		}
	default:
		return token, fmt.Errorf("message_cursor_invalid: unsupported or oversized message token")
	}
	account, err := hex.DecodeString(token.Account)
	if err != nil || len(account) != accountBytes || token.Talker == "" || len(token.Talker) > 1024 || !msgShardRE.MatchString(token.Shard) || filepath.Base(token.Shard) != token.Shard || token.LocalID <= 0 {
		return token, fmt.Errorf("message_cursor_invalid: invalid message token identity")
	}
	return token, nil
}

func parseMessageTokenV2(body string) (messageToken, bool) {
	var token messageToken
	parts := strings.SplitN(body, ".", 7)
	if len(parts) != 7 || len(parts[1]) < 2 {
		return token, false
	}
	switch parts[1][0] {
	case 'm':
		token.Shard = "message_" + parts[1][1:] + ".db"
	case 'b':
		token.Shard = "biz_message_" + parts[1][1:] + ".db"
	default:
		return token, false
	}
	nums := make([]int64, 4)
	for i := range nums {
		n, err := strconv.ParseInt(parts[2+i], 36, 64)
		if err != nil {
			return token, false
		}
		nums[i] = n
	}
	token.Account = parts[0]
	token.LocalID, token.ServerID, token.SortSeq, token.CreateTime = nums[0], nums[1], nums[2], nums[3]
	token.Talker = parts[6]
	return token, true
}

func encodeMessageToken(token messageToken) string {
	if m := messageTokenShardRE.FindStringSubmatch(token.Shard); m != nil && len(token.Account) >= 16 {
		kind := "m"
		if m[1] == "biz_message" {
			kind = "b"
		}
		return messageTokenV2Prefix + strings.Join([]string{
			token.Account[:16], kind + m[2],
			strconv.FormatInt(token.LocalID, 36), strconv.FormatInt(token.ServerID, 36),
			strconv.FormatInt(token.SortSeq, 36), strconv.FormatInt(token.CreateTime, 36),
			token.Talker,
		}, ".")
	}
	data, _ := json.Marshal(token)
	return messageTokenPrefix + base64.RawURLEncoding.EncodeToString(data)
}

// tokenAccountMatches accepts the full v1 account hash or the v2 prefix.
func (s *server) tokenAccountMatches(token messageToken) bool {
	full := s.messageTokenAccount()
	if full == "" {
		return false
	}
	return token.Account == full || (len(token.Account) == 16 && strings.HasPrefix(full, token.Account))
}

func (s *server) messageTokenAccount() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	root := filepath.Clean(s.cfg.DBRoot)
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	sum := sha256.Sum256([]byte(s.cfg.Wxid + "\x00" + root))
	return hex.EncodeToString(sum[:16])
}

func (s *server) attachMessageToken(talker, shard string, row wcdb.Row) {
	if row == nil || talker == "" || shard == "" || rowInt64(row, "local_id") <= 0 {
		return
	}
	row["_message_shard"] = shard
	token := messageToken{Account: s.messageTokenAccount(), Talker: talker, Shard: shard, LocalID: rowInt64(row, "local_id"), ServerID: rowInt64(row, "server_id"), SortSeq: rowInt64(row, "sort_seq"), CreateTime: rowInt64(row, "create_time")}
	if token.Account == "" {
		return
	}
	row["message_token"] = encodeMessageToken(token)
}

func (s *server) validateMessageTokenScope(raw, talker string) (messageToken, error) {
	token, err := parseMessageToken(raw)
	if err != nil {
		return token, err
	}
	if s.cfg == nil {
		if err := s.ensure(); err != nil {
			return token, err
		}
	}
	if !s.tokenAccountMatches(token) || token.Talker != talker {
		return token, fmt.Errorf("message_cursor_scope_mismatch: message token belongs to another account or conversation")
	}
	return token, nil
}

func (s *server) validateMessageBoundaryTokenScopes(a map[string]any, talker string) error {
	for _, key := range []string{"before_message_token", "after_message_token"} {
		if raw := getStr(a, key); raw != "" {
			if _, err := s.validateMessageTokenScope(raw, talker); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *server) findMessageTokenAnchor(shards []msgShardDB, tableName string, ref contextAnchorRef) (wcdb.Row, error) {
	token, err := parseMessageToken(ref.Token)
	if err != nil {
		return nil, err
	}
	if !s.tokenAccountMatches(token) || tableName != "Msg_"+talkerHash(token.Talker) {
		return nil, fmt.Errorf("message_cursor_scope_mismatch: message token belongs to another account or conversation")
	}
	if (ref.LocalID != 0 && ref.LocalID != token.LocalID) || (ref.ServerID != 0 && ref.ServerID != token.ServerID) {
		return nil, fmt.Errorf("message_cursor_mismatch: message token conflicts with the supplied local/server ID")
	}
	for _, warning := range msgShardWarnings(shards) {
		if strings.HasPrefix(warning, "message_shard_coverage_partial:") {
			return nil, fmt.Errorf("message_cursor_source_incomplete: cannot advance a message token while some message shards are unavailable")
		}
	}
	for i, shard := range shards {
		if shard.Name != token.Shard {
			continue
		}
		rows, err := s.queryContextRows(shards[i:i+1], tableName, "local_id = ? AND COALESCE(server_id, 0) = ? AND sort_seq = ? AND create_time = ?", []any{token.LocalID, token.ServerID, token.SortSeq, token.CreateTime}, "sort_seq ASC, local_id ASC, server_id ASC", 2)
		if err != nil {
			return nil, err
		}
		if len(rows) != 1 {
			return nil, fmt.Errorf("message_cursor_stale: original message anchor was deleted, changed, or is no longer unique; read a fresh timeline")
		}
		s.attachMessageToken(token.Talker, shard.Name, rows[0])
		return rows[0], nil
	}
	return nil, fmt.Errorf("message_cursor_stale: original message shard is no longer available; read a fresh timeline after deletion or migration")
}

type resolvedMessageBoundary struct {
	Direction string
	Anchor    wcdb.Row
}

func (s *server) resolveMessageBoundaries(a map[string]any, shards []msgShardDB, tableName string) ([]resolvedMessageBoundary, error) {
	var boundaries []resolvedMessageBoundary
	for _, direction := range []string{"before", "after"} {
		ref, ok, err := messageBoundaryRefFromArgs(a, direction)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		anchor, err := s.findContextAnchorRow(shards, tableName, ref)
		if err != nil {
			return nil, fmt.Errorf("%s_message anchor: %w", direction, err)
		}
		boundaries = append(boundaries, resolvedMessageBoundary{Direction: direction, Anchor: anchor})
	}
	return boundaries, nil
}
