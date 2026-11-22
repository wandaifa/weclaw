package terminal

import (
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{16,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{20,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{16,}\b`),
}

// RedactSecrets masks common API keys/tokens before terminal output leaves the host.
func RedactSecrets(text string) string {
	out := text
	for _, re := range secretPatterns {
		out = re.ReplaceAllStringFunc(out, func(tok string) string {
			if len(tok) <= 8 {
				return "***"
			}
			return tok[:4] + "…" + tok[len(tok)-4:]
		})
	}
	return strings.TrimRight(out, "\r\n")
}
