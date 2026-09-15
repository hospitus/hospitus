package main

import "strings"

// dedupeClauses removes clauses that a message already said earlier.
//
// The provider, the API and the CLI command each name the operation, which
// leaves the reason in fourth place:
//
//	failed to create snapshot: API error (500): Failed to create snapshot: exit
//	status 125: repository name must be lowercase
//
// Collapsing the repeats keeps both the path the error took and the reason:
//
//	failed to create snapshot: API error (500): exit status 125: repository name
//	must be lowercase
//
// Matching is case-insensitive but exact, so a clause naming a different path
// or instance survives, and a message with no repeat is returned unchanged.
func dedupeClauses(msg string) string {
	// ": " and not ":" — a URL, a port or a timestamp carries a colon with no
	// space after it, and splitting there would cut a clause in half.
	clauses := strings.Split(msg, ": ")
	if len(clauses) < 3 {
		return msg
	}

	seen := make(map[string]bool, len(clauses))
	kept := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		key := strings.ToLower(strings.TrimSpace(clause))
		if key != "" && seen[key] {
			continue
		}
		seen[key] = true
		kept = append(kept, clause)
	}

	return strings.Join(kept, ": ")
}
