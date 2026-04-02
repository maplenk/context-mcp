package tokenutil

import "regexp"

// camelCaseRe matches CamelCase word segments.
// Matches: sequences of uppercase+lowercase (e.g. "Read"), all-lowercase runs, or digit runs.
// Note: [A-Z]+ is unreachable because [A-Z][a-z0-9]* matches single uppercase letters first.
// SplitCamelCase compensates by merging consecutive single-uppercase tokens.
var camelCaseRe = regexp.MustCompile(`[A-Z][a-z0-9]*|[a-z][a-z0-9]*|[A-Z]+|[0-9]+`)

// SplitCamelCase splits a CamelCase identifier into words, correctly handling
// consecutive uppercase runs like "HTTPClient" -> ["HTTP", "Client"].
// Go's RE2 lacks lookaheads, so we use a two-pass approach:
//  1. Split with camelCaseRe (which yields ["H","T","T","P","Client"] for "HTTPClient")
//  2. Merge consecutive single-uppercase-letter tokens into one string
func SplitCamelCase(s string) []string {
	parts := camelCaseRe.FindAllString(s, -1)
	if len(parts) <= 1 {
		return parts
	}

	var merged []string
	i := 0
	for i < len(parts) {
		// Check if this is a single uppercase letter that starts a run
		if len(parts[i]) == 1 && parts[i][0] >= 'A' && parts[i][0] <= 'Z' {
			// Collect consecutive single-uppercase-letter tokens
			run := parts[i]
			j := i + 1
			for j < len(parts) && len(parts[j]) == 1 && parts[j][0] >= 'A' && parts[j][0] <= 'Z' {
				run += parts[j]
				j++
			}
			// If the next token starts with lowercase, the last uppercase letter
			// belongs to that token (e.g., "HTTP" + "Client" from "H","T","T","P","Client")
			if j < len(parts) && len(parts[j]) > 0 && parts[j][0] >= 'a' && parts[j][0] <= 'z' {
				// Only peel off the last letter if the run has more than 1 char
				if len(run) > 1 {
					merged = append(merged, run[:len(run)-1])
					// Prepend the last uppercase letter to the next token
					parts[j] = string(run[len(run)-1]) + parts[j]
				} else {
					// Single letter followed by lowercase token -- let regex result stand
					merged = append(merged, run+parts[j])
					j++
				}
			} else {
				merged = append(merged, run)
			}
			i = j
		} else {
			merged = append(merged, parts[i])
			i++
		}
	}
	return merged
}
