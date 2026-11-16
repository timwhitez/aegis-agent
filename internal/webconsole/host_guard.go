package webconsole

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
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
		if err := checkWebOriginHost(parsed.Hostname()); err != nil {
			return nil, fmt.Errorf("web.public_origins[%d] %w: %q", i, err, entry)
		}
		normalized, ok := normalizeWebOrigin(parsed)
		if !ok {
			return nil, fmt.Errorf("web.public_origins[%d] must use http or https with a host and a valid port: %q", i, entry)
		}
		origins[normalized] = struct{}{}
	}
	return origins, nil
}

// checkWebOriginHost rejects configured host spellings that a browser
// rewrites before sending them in Origin, so they could never match: non-ASCII
// names, sent in punycode, and IPv4 hosts not written in dotted decimal, which
// browsers parse as numbers (127.1, 0x7f.0.0.1 and 127.000.000.001 all become
// 127.0.0.1). IPv6 literals are canonicalized by normalizeWebOrigin instead.
func checkWebOriginHost(host string) error {
	host = strings.ToLower(host)
	for i := 0; i < len(host); i++ {
		if host[i] >= 0x80 {
			return errors.New("must write a non-ASCII host in its ASCII (punycode) form")
		}
	}
	if strings.Contains(host, ":") {
		return nil
	}
	// A host whose last label is a number is an IPv4 address to a browser.
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	last := labels[len(labels)-1]
	numeric := last != "" && strings.Trim(last, "0123456789") == ""
	if hex, ok := strings.CutPrefix(last, "0x"); ok {
		numeric = strings.Trim(hex, "0123456789abcdef") == ""
	}
	if !numeric {
		return nil
	}
	if addr, err := netip.ParseAddr(host); err == nil && addr.Is4() && addr.String() == host {
		return nil
	}
	return errors.New("must write an IPv4 host in dotted-decimal form such as 127.0.0.1")
}

// normalizeWebOrigin returns the canonical lowercase scheme://host[:port] form
// of an http(s) origin, dropping the scheme's default port the way browsers
// do in the Origin header. IPv6 literals are reduced to one canonical spelling
// so equal addresses compare equal. ok is false for other schemes, a missing
// host, a non-IP host containing ':', an IPv6 zone or an invalid port.
func normalizeWebOrigin(u *url.URL) (string, bool) {
	if u == nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.ContainsAny(host, "[]") {
		return "", false
	}
	if strings.Contains(host, ":") {
		addr, err := netip.ParseAddr(host)
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", false
		}
		host = addr.String()
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
