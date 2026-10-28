package service

import (
	"maps"
	"strings"
)

// IsLegacyCodexTicketExtraKey keeps retired tickets and proxy credentials private.
func IsLegacyCodexTicketExtraKey(key string) bool {
	return strings.HasPrefix(key, "codex_turn_ticket:") || key == "codex_harvest_proxy_url"
}

func RedactLegacyCodexTicketExtra(extra map[string]any) map[string]any {
	redacted := maps.Clone(extra)
	for key := range redacted {
		if IsLegacyCodexTicketExtraKey(key) {
			delete(redacted, key)
		}
	}
	return redacted
}
