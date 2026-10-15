// Package diagnostics sanitizes diagnostic text, never user message content.
package diagnostics

import "regexp"

var (
	authorization    = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization)\b["']?\s*[:=]\s*["']?(?:bearer|basic)\s+[^\s,"'}]+`)
	secretAssignment = regexp.MustCompile(`(?i)\b(password|sudo_password|password_hex|key_hex|enc_key|image_key|image_xor_key|derived_key|salt_hex|salt|token|access_token|refresh_token|api[_-]?key)\b["']?\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,}]+)`)
	hexMaterial      = regexp.MustCompile(`(?i)\b[0-9a-f]{32,128}\b`)
	accountID        = regexp.MustCompile(`\bwxid_[A-Za-z0-9_-]+\b`)
)

func Redact(text string) string {
	text = authorization.ReplaceAllString(text, "$1=[redacted]")
	text = secretAssignment.ReplaceAllString(text, "$1=[redacted]")
	text = hexMaterial.ReplaceAllString(text, "[redacted]")
	return accountID.ReplaceAllString(text, "[account]")
}
