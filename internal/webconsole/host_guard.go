package webconsole

import (
	"fmt"
	"net"
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
