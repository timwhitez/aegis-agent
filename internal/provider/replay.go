package provider

import "strings"

func providerReplayScopeMatches(stored, current string) bool {
	current = strings.TrimSpace(current)
	if current == "" {
		return true
	}
	return strings.TrimSpace(stored) == current
}
