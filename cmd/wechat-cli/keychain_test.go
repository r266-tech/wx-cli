package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/keystore"
)

func TestKeychainAuthorizeRequiresExplicitInteraction(t *testing.T) {
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
	s := &server{}
	for _, args := range []map[string]any{nil, {"interactive": false}} {
		if _, err := s.toolKeychainAuthorize(args); err == nil || !strings.Contains(err.Error(), "explicit --interactive") {
			t.Fatal("an implicit request must not prompt", err)
		}
	}
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	if _, err := s.toolKeychainAuthorize(map[string]any{"interactive": true}); err == nil || !strings.Contains(err.Error(), "strict_read_only") {
		t.Fatal("strict read-only allowed authorization", err)
	}
}

func TestKeychainDiagnosticKeepsKnownCauseAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{keystore.ErrInteractionRequired, "keychain_access_required"},
		{keystore.ErrAuthorizationDenied, "keychain_authorization_denied"},
		{keystore.ErrItemNotFound, "keychain_item_missing"},
		{keystore.ErrUnavailable, "keychain_unavailable"},
		{errors.New("private data"), "keychain_record_invalid"},
	} {
		if got := keychainDiagnosticCode(fmt.Errorf("wrapped: %w", tc.err)); got != tc.code {
			t.Errorf("code=%q, want %q", got, tc.code)
		}
	}
	advice := cliErrorAdvice("keychain_access_required", "", "chat_timeline", "timeline")
	if len(advice.SuggestedCommands) == 0 || advice.SuggestedCommands[0] != appName+" keychain authorize --interactive" {
		t.Fatal("recovery command missing")
	}
}

func TestKeychainMaintenanceToolsDescribeLocalWrites(t *testing.T) {
	for _, name := range []string{"keychain_status", "keychain_migrate", "keychain_authorize"} {
		if !toolInProfile(name, "maintenance") || toolInProfile(name, "assistant") {
			t.Errorf("incorrect profile for %s", name)
		}
	}
	for _, name := range []string{"keychain_migrate", "keychain_authorize"} {
		if toolLocalWriteMode(name) != "required" || toolStrictReadOnlyBehavior(name) != "blocked" {
			t.Errorf("incorrect write metadata for %s", name)
		}
	}
	if name, ok := callableToolNameForTarget("keychain authorize"); !ok || name != "keychain_authorize" {
		t.Fatal("authorize command schema missing")
	}
}
