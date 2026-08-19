package redact

import (
	"regexp"
	"strings"
)

var (
	bearerPattern = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]+`)
	urlPattern    = regexp.MustCompile(`(?i)(https?://)[^/\s]+(/[^\s]*)?`)
)

// String removes bearer tokens and subscription-like URLs from display text.
func String(input string) string {
	out := bearerPattern.ReplaceAllString(input, "Bearer [redacted]")
	out = urlPattern.ReplaceAllStringFunc(out, func(match string) string {
		if strings.Contains(strings.ToLower(match), "subscribe") ||
			strings.Contains(strings.ToLower(match), "token") {
			return "[redacted-url]"
		}
		return match
	})
	return out
}
