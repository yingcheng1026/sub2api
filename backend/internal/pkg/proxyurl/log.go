package proxyurl

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"strings"
)

const (
	directLogTarget     = "direct"
	configuredLogTarget = "configured"
)

// ForLog returns only the validated proxy endpoint. It never returns userinfo,
// path, query, fragment, or malformed input.
func ForLog(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return directLogTarget
	}
	_, parsed, err := Parse(raw)
	if err != nil || parsed == nil {
		return configuredLogTarget
	}
	return endpoint(parsed)
}

// TransportCacheKey distinguishes proxy credentials without retaining them in
// the key. The parsed URL remains unchanged for transport use.
func TransportCacheKey(parsed *url.URL) string {
	if parsed == nil {
		return directLogTarget
	}
	key := endpoint(parsed)
	if parsed.User == nil {
		return key
	}
	digest := sha256.Sum256([]byte(parsed.User.String()))
	return key + "|auth_sha256=" + hex.EncodeToString(digest[:])
}

func endpoint(parsed *url.URL) string {
	if parsed == nil {
		return directLogTarget
	}
	scheme := strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return (&url.URL{Scheme: scheme, Host: host}).String()
}
