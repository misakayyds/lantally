package redact

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	bearerPattern           = regexp.MustCompile(`(?i)bearer\s+[^\s"']+`)
	urlPattern              = regexp.MustCompile(`(?i)(?:https?|wss?)://[^\s"']+`)
	fallbackUserinfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)([^/?#\s"']+)(@)`)
	fallbackQueryPattern    = regexp.MustCompile(`(?i)([?&](?:token|secret|auth|key|password|pass|apikey|api_key|access_token|subscribe)=)[^&#\s"']+`)
)

var sensitiveQueryParams = map[string]struct{}{
	"token":        {},
	"secret":       {},
	"auth":         {},
	"key":          {},
	"password":     {},
	"pass":         {},
	"apikey":       {},
	"api_key":      {},
	"access_token": {},
	"subscribe":    {},
}

// String removes bearer tokens, URL credentials, sensitive query parameters,
// and subscription-like URLs from display text.
func String(input string) string {
	out := bearerPattern.ReplaceAllString(input, "Bearer [redacted]")
	out = urlPattern.ReplaceAllStringFunc(out, func(match string) string {
		if sanitized, changed, ok := sanitizeURL(match); ok && changed {
			return sanitized
		}
		if strings.Contains(strings.ToLower(match), "subscribe") {
			return "[redacted-url]"
		}
		return match
	})
	return out
}

// URL masks userinfo credentials and sensitive query parameters in rawURL
// while preserving scheme, host, port, and path for display.
func URL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	sanitized, _, ok := sanitizeURL(trimmed)
	if ok {
		return sanitized
	}
	return String(trimmed)
}

// Secrets applies String redaction and scrubs any explicit secret literals
// (and their URL-escaped forms) from input.
func Secrets(input string, secrets ...string) string {
	out := String(input)
	for _, sec := range secrets {
		if strings.TrimSpace(sec) == "" || len(sec) < 3 {
			continue
		}
		out = strings.ReplaceAll(out, sec, "[redacted]")
		if q := url.QueryEscape(sec); q != "" && q != sec {
			out = strings.ReplaceAll(out, q, "[redacted]")
		}
		if p := url.PathEscape(sec); p != "" && p != sec {
			out = strings.ReplaceAll(out, p, "[redacted]")
		}
	}
	return out
}

func sanitizeURL(rawURL string) (sanitized string, changed bool, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return sanitizeURLFallback(rawURL)
	}

	var maskedUser string
	if u.User != nil {
		changed = true
		username := u.User.Username()
		if _, hasPass := u.User.Password(); hasPass && username != "" {
			maskedUser = username + ":[redacted]"
		} else {
			maskedUser = "[redacted]"
		}
		u.User = nil
	}

	if u.RawQuery != "" {
		parts := strings.Split(u.RawQuery, "&")
		for i, part := range parts {
			if part == "" {
				continue
			}
			k, _, _ := strings.Cut(part, "=")
			decodedKey, decErr := url.QueryUnescape(k)
			if decErr != nil {
				decodedKey = k
			}
			if _, sensitive := sensitiveQueryParams[strings.ToLower(strings.TrimSpace(decodedKey))]; sensitive {
				changed = true
				parts[i] = k + "=[redacted]"
			}
		}
		u.RawQuery = strings.Join(parts, "&")
	}

	out := u.String()
	if maskedUser != "" {
		prefix := u.Scheme + "://"
		if strings.HasPrefix(out, prefix) {
			out = prefix + maskedUser + "@" + strings.TrimPrefix(out, prefix)
		}
	}
	return out, changed, true
}

func sanitizeURLFallback(rawURL string) (sanitized string, changed bool, ok bool) {
	out := fallbackUserinfoPattern.ReplaceAllStringFunc(rawURL, func(m string) string {
		parts := fallbackUserinfoPattern.FindStringSubmatch(m)
		if len(parts) != 4 {
			return m
		}
		prefix, userinfo, at := parts[1], parts[2], parts[3]
		changed = true
		if user, _, hasColon := strings.Cut(userinfo, ":"); hasColon && user != "" {
			return prefix + user + ":[redacted]" + at
		}
		return prefix + "[redacted]" + at
	})
	if fallbackQueryPattern.MatchString(out) {
		changed = true
		out = fallbackQueryPattern.ReplaceAllString(out, "${1}[redacted]")
	}
	if !changed {
		return "", false, false
	}
	return out, true, true
}
