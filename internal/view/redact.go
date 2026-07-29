package view

import (
	"net/url"
	"strings"
)

// Redaction masks values that look like credentials on the text path.
//
// This is not hypothetical: real events in this organization carry a `secret=`
// query parameter on a request URL and an `x-honeycomb-team` header — the
// Honeycomb ingest key — both live. Printing an event to a terminal, a log or a
// pull request comment should not leak them. A trace id, by contrast, identifies
// a trace and is not a secret, so it is left shown.
//
// --json is deliberately never redacted, and its help says so: the point of that
// path is that its values are exactly what the API returned.

// sensitiveNames are substrings that mark a key as holding a credential.
var sensitiveNames = []string{
	"cookie",
	"password",
	"passwd",
	"secret",
	"token",
	"api_key",
	"apikey",
	"api-key",
	"access_key",
	"private_key",
	"credential",
	"session",
	"csrf",
	"signature",
	"x-honeycomb-team",
	"bearer",
	"auth",
}

// redactValue masks value when its key looks sensitive.
//
// The length is kept so that "the header was present but empty" stays
// distinguishable from "the header held something".
func redactValue(key, value string, redact bool) string {
	if !redact || value == "" {
		return value
	}
	if !isSensitiveName(key) {
		return value
	}
	return mask(value)
}

// maybeRedactURL masks sensitive query parameters while leaving the rest of the
// URL readable, since the path is usually the useful part.
func maybeRedactURL(raw string, redact bool) string {
	if !redact || raw == "" {
		return raw
	}

	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}

	q := u.Query()
	changed := false
	for name, values := range q {
		if !isSensitiveName(name) && !isSensitiveQueryParam(name) {
			continue
		}
		for i := range values {
			values[i] = mask(values[i])
		}
		q[name] = values
		changed = true
	}
	if !changed {
		return raw
	}

	u.RawQuery = q.Encode()
	return u.String()
}

func isSensitiveName(key string) bool {
	lower := strings.ToLower(key)
	// Compare on the final path segment too, so a nested metadata key like
	// "request.headers.authorization" is caught.
	if i := strings.LastIndexAny(lower, ".["); i >= 0 {
		lower = lower[i+1:]
	}
	for _, needle := range sensitiveNames {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// isSensitiveQueryParam covers the one name isSensitiveName's substring match
// cannot: a bare "key" is shorter than every key-ish entry in that list.
func isSensitiveQueryParam(name string) bool {
	return strings.EqualFold(name, "key")
}

// mask replaces a non-empty value with a fixed marker. An empty value is left
// empty, so "the header was present but empty" stays distinguishable from a
// value that was redacted.
func mask(value string) string {
	if value == "" {
		return ""
	}
	return "[redacted]"
}
