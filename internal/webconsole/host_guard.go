package webconsole

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// webHostAllowlistEntry is one explicitly configured trusted Host value. A
// bare host matches any port; a host:port entry matches only that port.
type webHostAllowlistEntry struct {
	host string
	port string
}

type webHostAllowlist []webHostAllowlistEntry

// parseWebHostAllowlist validates operator-configured `web.allowed_hosts`
// entries. Entries must be bare hosts or host:port pairs (IPv6 hosts in
// brackets when a port is present); schemes, paths, user info and wildcards
// are rejected so an entry can never smuggle in an unrelated authority.
func parseWebHostAllowlist(entries []string) (webHostAllowlist, error) {
	parsed := make(webHostAllowlist, 0, len(entries))
	for i, entry := range entries {
		trimmed := strings.ToLower(strings.TrimSpace(entry))
		if trimmed == "" {
			return nil, fmt.Errorf("web.allowed_hosts[%d] must not be empty", i)
		}
		for _, forbidden := range []string{"/", "\\", "?", "#", "@", "*", " "} {
			if strings.Contains(trimmed, forbidden) {
				return nil, fmt.Errorf("web.allowed_hosts[%d] must be a host or host:port value without %q: %q", i, forbidden, entry)
			}
		}
		if scheme, _, found := strings.Cut(trimmed, "://"); found {
			return nil, fmt.Errorf("web.allowed_hosts[%d] must not include a scheme (%s): %q", i, scheme, entry)
		}
		host, port := trimmed, ""
		if strings.Contains(trimmed, ":") {
			splitHost, splitPort, err := net.SplitHostPort(trimmed)
			if err != nil {
				return nil, fmt.Errorf("web.allowed_hosts[%d] must be host or bracketed host:port: %q", i, entry)
			}
			portNumber, err := strconv.Atoi(splitPort)
			if err != nil || portNumber < 1 || portNumber > 65535 {
				return nil, fmt.Errorf("web.allowed_hosts[%d] has an invalid port: %q", i, entry)
			}
			host, port = splitHost, splitPort
		}
		host = strings.Trim(strings.TrimSpace(host), "[]")
		if host == "" {
			return nil, fmt.Errorf("web.allowed_hosts[%d] must include a host: %q", i, entry)
		}
		parsed = append(parsed, webHostAllowlistEntry{host: host, port: port})
	}
	return parsed, nil
}

// matches reports whether hostport equals a configured entry. Matching is
// exact-host (no suffix semantics), so `evil.example` never matches an
// `example` entry.
func (l webHostAllowlist) matches(hostport string) bool {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return false
	}
	host, port := hostport, ""
	if splitHost, splitPort, err := net.SplitHostPort(hostport); err == nil {
		host, port = splitHost, splitPort
	}
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" {
		return false
	}
	for _, entry := range l {
		if entry.host != host {
			continue
		}
		if entry.port == "" || entry.port == port {
			return true
		}
	}
	return false
}

// webPublicOrigins is the set of operator-declared browser-facing origins
// (web.public_origins), normalized by normalizeWebOrigin.
type webPublicOrigins map[string]struct{}

// parseWebPublicOrigins validates web.public_origins. Each entry must be an
// absolute http or https origin with a host and an optional valid port, and
// nothing else: no user info, path, query, fragment or wildcard.
func parseWebPublicOrigins(entries []string) (webPublicOrigins, error) {
	origins := make(webPublicOrigins, len(entries))
	for i, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			return nil, fmt.Errorf("web.public_origins[%d] must not be empty", i)
		}
		if strings.ContainsAny(trimmed, "*\\ ") {
			return nil, fmt.Errorf("web.public_origins[%d] must be a plain scheme://host[:port] origin: %q", i, entry)
		}
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return nil, fmt.Errorf("web.public_origins[%d] is not a valid origin: %q", i, entry)
		}
		if parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return nil, fmt.Errorf("web.public_origins[%d] must be scheme://host[:port] without user info, path, query or fragment: %q", i, entry)
		}
		normalized, ok := normalizeWebOrigin(parsed)
		if !ok {
			return nil, fmt.Errorf("web.public_origins[%d] must use http or https with a host and a valid port: %q", i, entry)
		}
		origins[normalized] = struct{}{}
	}
	return origins, nil
}

// normalizeWebOrigin returns the canonical lowercase scheme://host[:port] form
// of an http(s) origin, dropping the scheme's default port the way browsers
// do in the Origin header. ok is false for other schemes, a missing host, a
// non-IP host containing ':' or an invalid port.
func normalizeWebOrigin(u *url.URL) (string, bool) {
	if u == nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.ContainsAny(host, "[]") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return "", false
	}
	port := u.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", false
		}
		port = strconv.Itoa(number)
		if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
			port = ""
		}
	}
	switch {
	case port != "":
		return scheme + "://" + net.JoinHostPort(host, port), true
	case strings.Contains(host, ":"):
		return scheme + "://[" + host + "]", true
	default:
		return scheme + "://" + host, true
	}
}

// contains reports whether a browser Origin equals a configured public origin.
func (o webPublicOrigins) contains(origin *url.URL) bool {
	if len(o) == 0 || origin == nil {
		return false
	}
	normalized, ok := normalizeWebOrigin(origin)
	if !ok {
		return false
	}
	_, found := o[normalized]
	return found
}
