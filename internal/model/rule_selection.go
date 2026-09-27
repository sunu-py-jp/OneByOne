package model

import (
	"sort"
	"strings"
)

// NormalizeExcludedRuleIDs keeps only current, distinct IDs. Rules newly added
// to a workspace are selected by default, and removed IDs cannot disable a
// future rule accidentally. A nil knownIDs list only canonicalizes the values.
func NormalizeExcludedRuleIDs(excluded, knownIDs []string) []string {
	known := map[string]bool{}
	for _, id := range knownIDs {
		known[id] = true
	}
	seen := map[string]bool{}
	for _, id := range excluded {
		id = strings.TrimSpace(id)
		if id != "" && (knownIDs == nil || known[id]) {
			seen[id] = true
		}
	}
	var result []string
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}
