package main

import (
	"os"
	"testing"
)

// CLI unit tests must never load the developer's configuration or Keychain.
// Individual tests can still override HOME with t.Setenv.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "wechat-cli-unit-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("USERPROFILE", home)
	// Reads auto-migrate plaintext keys into the real login Keychain; unit
	// tests must not, whatever HOME says.
	_ = os.Setenv("WECHAT_CLI_NO_KEYCHAIN_MIGRATE", "1")
	for _, key := range []string{"WECHAT_CLI_CONFIG", "WX_MCP_CONFIG", "WECHAT_CLI_HOME", "WX_MCP_HOME", "WECHAT_CLI_STATE_DIR", "WX_MCP_STATE_DIR", "WECHAT_CLI_DB_ROOT", "WX_MCP_DB_ROOT"} {
		_ = os.Unsetenv(key)
	}
	result := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(result)
}
