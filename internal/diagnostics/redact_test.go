package diagnostics

import (
	"strings"
	"testing"
)

func TestRedactDiagnosticsPreservesRecoveryWithoutSecrets(t *testing.T) {
	secret := strings.Repeat("e1", 32)
	salt := strings.Repeat("a2", 16)
	input := `password="fixture password" enc_key=` + secret + ` salt ` + salt + ` wxid_fixture_account; run wxkey doctor`
	got := Redact(input)
	for _, value := range []string{"fixture password", secret, salt, "wxid_fixture_account"} {
		if strings.Contains(got, value) {
			t.Fatal("diagnostic secret leaked")
		}
	}
	if !strings.Contains(got, "run wxkey doctor") {
		t.Fatal("recovery hint removed")
	}
}
