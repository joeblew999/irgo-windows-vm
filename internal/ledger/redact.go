package ledger

import (
	"os"
	"regexp"
	"strings"
)

var (
	userinfo = regexp.MustCompile(`://[^/\s@]+@`)
	secretKV = regexp.MustCompile(`(?i)\b(bearer|token|secret|password|passwd|pwd|api[_-]?key|access[_-]?key|authorization)(\s*[=:]\s*|\s+)\S+`)
	opaque   = regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`)
)

// Redact takes out of s what must not leave this machine: the home directory
// (it names the user), credentials in URLs, values after words such as token
// or password, and any long opaque run that could be a key. Hashes and UUIDs
// go too; the ledger records what happened, not to which bytes.
func Redact(s string) string {
	if s == "" {
		return s
	}
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		s = strings.ReplaceAll(s, home, "~")
	}
	s = userinfo.ReplaceAllString(s, "://[redacted]@")
	s = secretKV.ReplaceAllString(s, "$1$2[redacted]")
	return opaque.ReplaceAllString(s, "[redacted]")
}
