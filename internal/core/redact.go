package core

import (
	"regexp"
	"strings"
)

const redacted = "[redacted]"

var (
	bearerPattern = regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	keyPattern    = regexp.MustCompile(`(?i)\b([a-z0-9_]*(?:api[_-]?key|token|secret|password|passwd|authorization))("?\s*[:=]\s*"?)([^\s"',]+)`)
	valuePatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	}
)

// Redact hides secrets in text that leaves acp through scan, web or MCP: bearer tokens, values of
// key/token/secret/password fields and well-known token shapes.
func Redact(s string) string {
	s = bearerPattern.ReplaceAllString(s, "${1}"+redacted)
	s = keyPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := keyPattern.FindStringSubmatch(m)
		if v := strings.ToLower(sub[3]); v == "bearer" || v == redacted {
			return m
		}
		return sub[1] + sub[2] + redacted
	})
	for _, p := range valuePatterns {
		s = p.ReplaceAllString(s, redacted)
	}
	return s
}

// RedactValue applies Redact to every string inside a JSON value.
func RedactValue(v any) any {
	switch t := v.(type) {
	case string:
		return Redact(t)
	case []any:
		out := make([]any, len(t))
		for i, it := range t {
			out[i] = RedactValue(it)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, it := range t {
			out[k] = RedactValue(it)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, it := range t {
			out[i] = Redact(it)
		}
		return out
	case *Object:
		out := NewObject()
		for _, k := range t.Keys() {
			val, _ := t.Get(k)
			out.Set(k, RedactValue(val))
		}
		return out
	}
	return v
}
