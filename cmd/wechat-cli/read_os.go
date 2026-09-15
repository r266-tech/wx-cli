package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/r266-tech/wx-cli/v2/internal/config"
	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
	"github.com/r266-tech/wx-cli/v2/internal/wxkey"
)

func (s *server) toolReadOS(a map[string]any) (any, error) {
	mode := getStr(a, "mode")
	if mode == "" {
		mode = "overview"
	}
	includeStatus := getBoolDefault(a, "include_status", true)
	includeDebug := includeDebugOutput(a)
	out := map[string]any{
		"identity": map[string]any{
			"name":        appName,
			"version":     appVersion,
			"goal":        "read-only WeChat data OS for agents",
			"contract":    "WeChat read only; no sending, no UI control, no WeChat data mutation. Set WECHAT_CLI_STRICT_READ_ONLY=1 to also disable local support-file writes.",
			"data_policy": "message bodies are live-read from local WeChat DBs; metadata cache is contacts/sessions only",
		},
	}
	switch mode {
	case "overview":
		if includeStatus {
			out["status"] = s.readOSStatus(includeDebug)
		}
		out["entrypoints"] = readOSEntrypoints()
		out["workflows"] = readOSWorkflows()
		out["coverage"] = readOSCoverageMatrix()
		out["quality_gates"] = readOSQualityGates()
	case "coverage":
		out["coverage"] = readOSCoverageMatrix()
	case "workflows":
		out["entrypoints"] = readOSEntrypoints()
		out["workflows"] = readOSWorkflows()
	case "status":
		out["status"] = s.readOSStatus(includeDebug)
	case "doctor":
		out["doctor"] = s.readOSDoctor()
	default:
		return nil, errInvalidReadOSMode(mode)
	}
	return out, nil
}

func errInvalidReadOSMode(mode string) error {
	return fmt.Errorf("invalid mode=%q: must be overview / coverage / workflows / status / doctor", mode)
}

// readOSDoctor reports executable and local-runtime provenance without
// refreshing keys, metadata, or message data. It exists to catch the common
// failure mode where PATH, a shim, a source checkout, and an installed release
// point at different wechat-cli binaries.
func (s *server) readOSDoctor() map[string]any {
	status := s.readOSStatus(false)
	data := map[string]any{
		"checked_at": time.Now().Format(time.RFC3339),
		"platform": map[string]any{
			"os":   runtime.GOOS,
			"arch": runtime.GOARCH,
		},
		"runtime": map[string]any{
			"app_name":           appName,
			"source_commit":      sourceCommit,
			"source_repository":  "r266-tech/wx-cli",
			"release_repository": "r266-tech/wechat-cli-releases",
			"app_version":        appVersion,
			"strict_read_only":   strictReadOnlyMode(),
		},
		"status": status,
	}

	if exe, err := os.Executable(); err == nil {
		data["runtime"].(map[string]any)["current_executable"] = exe
		data["runtime"].(map[string]any)["current_executable_realpath"] = realPath(exe)
	}
	pathCandidates := []map[string]any{}
	seenPaths := map[string]bool{}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		candidate := filepath.Join(entry, appName)
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		candidateRealpath := realPath(candidate)
		if seenPaths[candidateRealpath] {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			version := binaryVersion(candidate)
			seenPaths[candidateRealpath] = true
			pathCandidates = append(pathCandidates, map[string]any{
				"path":     candidate,
				"realpath": candidateRealpath,
				"version":  version,
			})
		}
	}
	data["runtime"].(map[string]any)["path_candidates"] = pathCandidates
	if resolved, err := exec.LookPath(appName); err == nil {
		data["runtime"].(map[string]any)["path_resolution"] = map[string]any{
			"path":     resolved,
			"realpath": realPath(resolved),
		}
	}

	paths := map[string]any{}
	data["paths"] = paths
	if cfgPath, err := config.Path(); err == nil {
		paths["config"] = cfgPath
	}
	if stateDir, err := appStateDir(); err == nil {
		paths["state"] = stateDir
		if cache, err := cachePathsFor(&config.Config{}); err == nil {
			paths["cache_root"] = cache.RootDir
			paths["cache_index"] = cache.IndexPath
		}
	}
	if wxkeyPath, err := wxkey.FindBinary(); err == nil {
		data["components"] = map[string]any{
			"wxkey": map[string]any{
				"path":     wxkeyPath,
				"realpath": realPath(wxkeyPath),
			},
		}
	} else {
		data["components"] = map[string]any{"wxkey": map[string]any{"available": false, "error": err.Error()}}
	}
	if wcdbPath, err := findWCDB(); err == nil {
		components := data["components"].(map[string]any)
		components["wcdb"] = map[string]any{"path": wcdbPath, "realpath": realPath(wcdbPath)}
	} else {
		components := data["components"].(map[string]any)
		components["wcdb"] = map[string]any{"available": false, "error": err.Error()}
	}

	warnings := []string{}
	runtimeInfo := data["runtime"].(map[string]any)
	if len(pathCandidates) > 1 {
		warnings = append(warnings, "multiple_path_binaries")
	}
	if resolved, ok := runtimeInfo["path_resolution"].(map[string]any); ok {
		if current, ok := runtimeInfo["current_executable_realpath"].(string); ok && current != "" {
			if path, _ := resolved["realpath"].(string); path != "" && path != current {
				warnings = append(warnings, "path_binary_differs_from_current_executable")
			}
		}
	}
	if status["readiness"] != "ready" {
		warnings = append(warnings, "runtime_not_ready")
	}
	data["warnings"] = warnings
	data["next_actions"] = []string{
		"Use the canonical installed shim after resolving path_candidates.",
		"Run status and sessions after correcting any version or component mismatch.",
	}
	return data
}

func binaryVersion(path string) string {
	cmd := exec.Command(path, "--version")
	cmd.Env = append(os.Environ(), "WECHAT_CLI_STRICT_READ_ONLY=1")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	var envelope struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if json.Unmarshal(output, &envelope) == nil && envelope.Data.Version != "" {
		return envelope.Data.Version
	}
	return "unknown"
}

func realPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolved
}

func (s *server) readOSStatus(includeDebug bool) map[string]any {
	capabilities := readOSCapabilities(false, false, false)
	status := map[string]any{
		"platform": map[string]any{
			"os":   runtime.GOOS,
			"arch": runtime.GOARCH,
		},
		"mode": map[string]any{
			"strict_read_only": strictReadOnlyMode(),
		},
		"key_store":    "missing",
		"capabilities": capabilities,
		"checked_at":   time.Now().Format(time.RFC3339),
	}
	readiness := "ready"
	dbReady := false
	cacheIndexExists := false
	wcdbAvailable := false
	var warnings []string
	var degradedBy []string
	blockedBy := ""
	nextAction := ""
	var suggestedCommands []string
	setBlocked := func(by, action string, commands ...string) {
		readiness = "blocked"
		if blockedBy != "" {
			return
		}
		blockedBy = by
		nextAction = action
		suggestedCommands = append(suggestedCommands, commands...)
	}
	cfgPath, cfgPathErr := config.Path()
	cfg, cfgErr := s.activeConfigNoSetup()
	if cfgErr != nil {
		status["config_error"] = cfgErr.Error()
		setBlocked("config_error", "Run first key setup, then rerun status.", readOSBootstrapCommand(), appName+" status --pretty")
	} else {
		if cfg.KeyStore == "keychain" {
			status["key_store"] = "keychain"
		} else if len(cfg.Keys) > 0 {
			status["key_store"] = "config_legacy"
		}
		status["account"] = compactMap(map[string]any{
			"identity_configured": cfg.Wxid != "",
			"db_root_configured":  cfg.DBRoot != "",
			"schema2_key_count":   len(cfg.Keys),
			"schema2_ready":       cfg.Ready(),
			"image_key_ready":     cfg.ImageKey != "" || cfg.ImageXORKey != nil,
		})
		if includeDebug {
			status["account_debug"] = compactMap(map[string]any{
				"wxid":    cfg.Wxid,
				"db_root": cfg.DBRoot,
			})
		}
		if cfg.DBRoot == "" {
			setBlocked("db_root_missing", "Configure WeChat DB root through first key setup.", readOSBootstrapCommand(), appName+" status --pretty")
		} else if !cfg.Ready() {
			setBlocked("key_config_missing", "Prepare local WeChat DB keys, then rerun the read command.", readOSBootstrapCommand(), appName+" status --pretty")
		} else {
			dbReady = true
		}
		if paths, err := cachePathsFor(cfg); err == nil {
			cacheIndexExists = fileExists(paths.IndexPath)
			cache := map[string]any{
				"index_exists": cacheIndexExists,
			}
			if includeDebug {
				cache["root_dir"] = paths.RootDir
				cache["index_path"] = paths.IndexPath
			}
			if cfg.DBRoot != "" {
				if dbs, err := listSourceDBs(cfg, paths); err == nil {
					cache["source_db_count"] = len(dbs)
				}
				if wcdbPath, err := findWCDB(); err == nil {
					if err := wcdb.Bootstrap(wcdbPath); err == nil {
						if fresh, reason, err := s.cacheFreshness(cfg, paths); err == nil && !fresh && reason != "" {
							cache["metadata_stale_reason"] = metadataStatusReason(reason)
							if staleCacheIndexUsable(reason) {
								cache["degraded"] = true
								degradedBy = appendUniqueStrings(degradedBy, "metadata_cache_stale")
								warnings = appendUniqueStrings(warnings, "metadata_cache_degraded")
								if readiness == "ready" {
									readiness = "degraded"
								}
							} else {
								cache["blocked_reason"] = metadataStatusReason(reason)
								cache["degraded"] = true
								degradedBy = appendUniqueStrings(degradedBy, "metadata_cache_degraded")
								warnings = appendUniqueStrings(warnings, "metadata_cache_degraded")
								if readiness == "ready" {
									readiness = "degraded"
								}
							}
						}
					}
				}
			}
			status["metadata_cache"] = cache
		}
	}
	if cfgPathErr == nil && includeDebug {
		status["config_path"] = cfgPath
	}
	if wcdbPath, err := findWCDB(); err == nil {
		wcdbAvailable = true
		status["wcdb"] = compactMap(map[string]any{
			"available": true,
			"path":      debugOnlyString(wcdbPath, includeDebug),
		})
	} else {
		status["wcdb"] = map[string]any{
			"available": false,
			"error":     err.Error(),
		}
		setBlocked("wcdb_missing", "Use an installed release or point WECHAT_CLI_WCDB_DYLIB/WECHAT_CLI_WCDB_LIB at the bundled WCDB library.", appName+" status --pretty")
	}
	capabilities = readOSCapabilities(dbReady, wcdbAvailable, cacheIndexExists)
	status["capabilities"] = capabilities
	liveReadOK := false
	if dbReady && wcdbAvailable {
		// A configured key map and an existing dylib are not a successful read.
		// Probe without setup, refresh, or other support-file writes.
		wcdbPath, _ := findWCDB()
		probe := &server{cfg: cfg, wcdbPath: wcdbPath, ok: true}
		db, err := probe.openDBNoRefresh("session", "session.db")
		if err == nil {
			_, err = db.Query("SELECT username FROM SessionTable LIMIT 1")
			db.Close()
		}
		liveReadOK = err == nil
		if err != nil {
			setBlocked("live_read_probe_failed", "Inspect doctor and keychain status, then retry after restoring access to the configured account.", appName+" doctor --full", appName+" keychain status")
		}
	}
	status["live_read_ok"] = liveReadOK
	status["capabilities"] = readOSCapabilities(liveReadOK, wcdbAvailable, cacheIndexExists)
	status["readiness"] = readiness
	if len(degradedBy) > 0 {
		status["degraded_by"] = degradedBy
	}
	if len(warnings) > 0 {
		status["warnings"] = warnings
	}
	if blockedBy != "" {
		status["blocked_by"] = blockedBy
		status["next_action"] = nextAction
		status["suggested_commands"] = suggestedCommands
	}
	return status
}

func readOSCapabilities(dbReady, wcdbAvailable, cacheIndexExists bool) map[string]bool {
	liveRead := dbReady && wcdbAvailable
	return map[string]bool{
		"search":          liveRead,
		"sessions":        liveRead,
		"contact_labels":  liveRead,
		"timeline":        liveRead,
		"context":         liveRead,
		"tail":            liveRead,
		"media":           liveRead,
		"voice_asr":       asrReadyBool(asrStatusData()["wechat_voice_ready"]),
		"name_resolution": liveRead,
	}
}

func readOSBootstrapCommand() string {
	if runtime.GOOS == "windows" {
		return appName + ".exe cache refresh --force"
	}
	return "~/.local/share/" + appName + "/wxkey bootstrap"
}

func debugOnlyString(s string, includeDebug bool) string {
	if includeDebug {
		return s
	}
	return ""
}

func readOSEntrypoints() []map[string]any {
	return []map[string]any{
		{"command": "agent", "tool": "read_os", "use": "agent-first entrypoint for capability matrix, workflows, install/readiness status"},
		{"command": "status", "tool": "read_os", "use": "quick local readiness check"},
		{"command": "coverage", "tool": "read_os", "use": "coverage matrix only"},
		{"command": "workflows", "tool": "read_os", "use": "command recipes only"},
		{"command": "asr status", "tool": "asr", "use": "check optional local voice transcription runtime"},
		{"command": "asr setup", "tool": "asr", "use": "install optional faster-whisper and SILK decode support in a local venv", "local_file_write": true},
		{"command": "sessions", "tool": "sessions", "use": "list recent chats and unread counts"},
		{"command": "resolve-chat", "tool": "resolve_chat", "use": "turn a human name/group name into a stable talker id"},
		{"command": "contacts", "tool": "contacts", "use": "find contacts, return each contact's labels, or filter contacts by label name/id"},
		{"command": "labels", "tool": "contact_labels", "use": "list contact labels and contact counts"},
		{"command": "timeline", "tool": "chat_timeline", "use": "read a chat window in display order; page with query.cursor.next_before_message"},
		{"command": "context", "tool": "message_context", "use": "expand before/after messages around a known local_id or server_id"},
		{"command": "tail", "tool": "read_events", "use": "read-only event tail for new chat messages or session/unread changes"},
		{"command": "search", "tool": "search", "use": "global or scoped keyword search over WeChat FTS"},
		{"command": "search-context", "tool": "search_with_context", "use": "search keyword hits and return surrounding context in one call"},
		{"command": "media", "tool": "media_resources", "use": "resolve images, videos, and files from timeline/search ids"},
		{"command": "members", "tool": "group_members", "use": "read group members and display names"},
		{"command": "export", "tool": "export_messages", "use": "explicit local file export for large single-chat reads", "local_file_write": true},
	}
}

func readOSWorkflows() []map[string]any {
	return []map[string]any{
		{
			"name": "read_recent_chat",
			"commands": []string{
				`wechat-cli resolve-chat "$CHAT"`,
				`wechat-cli timeline "$CHAT" --limit 50 --display-order asc`,
			},
		},
		{
			"name": "page_full_chat",
			"commands": []string{
				`wechat-cli timeline "$CHAT" --limit 200`,
				`repeat with --before-message <data.query.cursor.next_before_message> while data.query.has_more`,
			},
		},
		{
			"name":             "export_large_chat",
			"local_file_write": true,
			"commands": []string{
				`wechat-cli export "$CHAT" --path /tmp/chat.jsonl --format jsonl`,
			},
		},
		{
			"name": "search_then_expand",
			"commands": []string{
				`wechat-cli search "$KEYWORD" --limit 20`,
				`wechat-cli context "$CHAT" --local-id <result.id.local_id> --before-count 20 --after-count 20`,
				`wechat-cli search-context "$KEYWORD" --in "$CHAT" --context-limit 3`,
			},
		},
		{
			"name": "observe_incremental",
			"commands": []string{
				`wechat-cli tail "$CHAT" --since-local-id <last_seen_local_id> --jsonl`,
				`wechat-cli watch --mode sessions --cursor <last_session_cursor> --jsonl`,
			},
		},
		{
			"name": "inspect_media",
			"commands": []string{
				`wechat-cli timeline "$CHAT" --type image --limit 20`,
				`wechat-cli media "$CHAT" --local-id <message.id.local_id> --include-debug`,
			},
		},
		{
			"name": "group_read",
			"commands": []string{
				`wechat-cli members "群名" --limit 200`,
				`wechat-cli search "$KEYWORD" --in "$CHAT" --sender "$SENDER"`,
			},
		},
		{
			"name": "contact_labels",
			"commands": []string{
				`wechat-cli contacts --keyword "$CONTACT" --limit 10`,
				`wechat-cli labels --keyword "$LABEL"`,
				`wechat-cli contacts --label "$LABEL" --limit 50`,
			},
		},
	}
}

func readOSQualityGates() []map[string]any {
	return []map[string]any{
		qualityGate("install_smoke", `wechat-cli sessions --limit 5 --pretty`, "ok=true and recent sessions returned"),
		qualityGate("agent_entrypoint", `wechat-cli agent --pretty`, "coverage/workflows/status visible from one command"),
		qualityGate("chat_navigation", `wechat-cli timeline "$CHAT" --limit 20`, "query.has_more/query.cursor.next_before_message usable for stable paging"),
		qualityGate("around_context", `wechat-cli context "$CHAT" --local-id "$LOCAL_ID" --before-count 5 --after-count 5`, "anchor plus surrounding messages returned in chronological order"),
		qualityGate("anchor_timeline", `wechat-cli timeline "$CHAT" --before-message "$LOCAL_ID" --limit 10`, "message page can use an anchor instead of only offset"),
		qualityGate("event_tail", `wechat-cli tail "$CHAT" --since-local-id "$LOCAL_ID" --jsonl`, "newer messages, if any, emit as JSONL events with timeline-shaped event.message"),
		qualityGate("search_expand", `wechat-cli search "$KEYWORD" --in "$CHAT" --limit 5`, "search rows carry local_id/talker for context expansion"),
		qualityGate("search_with_context", `wechat-cli search-context "$KEYWORD" --in "$CHAT" --context-limit 3`, "keyword hits include before/anchor/after context windows"),
		qualityGate("media_readability", `wechat-cli media "$CHAT" --type image --limit 10`, "readable local paths or actionable warnings, never opaque .dat as a fake image path"),
		qualityGate("group_members", `wechat-cli members "$CHAT" --limit 50`, "group member display names resolve"),
		qualityGate("contact_labels", `wechat-cli labels`, "labels include stable ids, names, sort order, and contact counts; contacts --label returns members"),
		qualityGate("export_shape", `wechat-cli export "$CHAT" --path "$TMP/chat.jsonl" --format jsonl`, "agent-view JSONL rows match timeline message shape"),
	}
}

func qualityGate(name, command, pass string) map[string]any {
	out := map[string]any{
		"name":    name,
		"gate":    name,
		"command": command,
		"pass":    pass,
	}
	if name == "export_shape" {
		out["local_file_write"] = true
	}
	return out
}

func readOSCoverageMatrix() []map[string]any {
	return []map[string]any{
		coverageRow("text", "supported", "timeline/search/export", "text, sender, time, ids", "raw message_content, parser fields"),
		coverageRow("image", "supported_with_diagnostics", "timeline/media", "best readable local image path, warnings", "candidate paths, .dat decode status, image-key refresh diagnostics"),
		coverageRow("video", "supported_with_diagnostics", "timeline/media", "readable mp4/thumb paths when local cache exists", "resource ids, variants, raw local path details"),
		coverageRow("voice", "supported_with_diagnostics", "timeline", "voice payload and local ASR transcript when available", "raw SILK/cache path/transcription diagnostics"),
		coverageRow("file", "supported", "timeline/media", "file name, size, readable local path when present", "appattach/raw XML/resource details"),
		coverageRow("link", "supported", "timeline/search", "title, url, source, description/thumb when present", "raw appmsg XML"),
		coverageRow("quote", "supported", "timeline/context", "reply text plus referenced message summary/payload", "nested refermsg XML and parsed fields"),
		coverageRow("forward_chat", "supported_with_diagnostics", "timeline/media", "recursive forward items, text/link/file/image refs when resolvable", "source ids, nested raw items, unresolved media warnings"),
		coverageRow("location", "supported", "timeline/search", "label/poi/lat/lon/scale", "raw location XML"),
		coverageRow("transfer", "supported", "timeline/transfers", "amount, description, payer/receiver, message id", "pay XML/native fields"),
		coverageRow("red_packet", "supported", "timeline/red-packets", "wishing, scene text, sender/session ids", "native URL/raw XML"),
		coverageRow("solitaire", "supported", "timeline/search", "display-ready app payload summary", "raw appmsg XML"),
		coverageRow("miniprogram", "supported", "timeline/search", "title, app id/source, URL/thumb when present", "raw appmsg XML"),
		coverageRow("card", "supported", "timeline/search", "shared contact/card display payload", "raw card XML"),
		coverageRow("sticker", "supported_with_diagnostics", "timeline/media", "sticker summary and media hints when present", "raw emoji XML/media candidates"),
		coverageRow("system_recall", "supported", "timeline/search", "system/recall text as visible in WeChat", "raw system content"),
		coverageRow("group_members", "supported", "members", "username, display name, owner/friend flags", "member stats when requested"),
		coverageRow("contact_labels", "supported", "contacts/labels", "contact labels as id/name refs and bidirectional contact filtering", "ContactInfo protobuf field 30 plus contact_label definitions"),
		coverageRow("moments", "supported", "sns-feed/sns-search/sns-notifications", "posts, comments, likes, media metadata", "raw SNS XML/media keys"),
		coverageRow("favorites", "supported", "favorites", "favorite type, source, title/description/url", "raw favorite XML"),
		coverageRow("chatroom_announcements", "supported", "announcements", "announcement, editor, publish time", "raw contact DB fields"),
	}
}

func coverageRow(kind, status, entrypoint, agentDefault, debug string) map[string]any {
	return map[string]any{
		"kind":          kind,
		"status":        status,
		"entrypoint":    entrypoint,
		"agent_default": agentDefault,
		"debug":         debug,
	}
}
