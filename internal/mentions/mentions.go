// Package mentions extracts the leading @name run from a message.
package mentions

import (
	"strings"

	"github.com/NoRaincheck/fluffle/internal/names"
)

// Parse returns the distinct names in a leading @name run and the request
// that follows it. The name charset, not a delimiter, ends a token, and a
// token that a looser charset would have continued is not a name at all, so
// @alice2 stays prose.
func Parse(content string) (found []string, request string) {
	rest := content
	seen := map[string]bool{}
	for {
		trimmed := strings.TrimLeft(rest, " \t\n\r")
		if !strings.HasPrefix(trimmed, "@") {
			break
		}
		token, width := scanName(trimmed[1:])
		if width == 0 {
			break
		}
		name := strings.TrimRight(token, ".")
		if name != "" && !seen[name] {
			seen[name] = true
			found = append(found, name)
		}
		rest = trimmed[1+width:]
	}
	if len(found) == 0 {
		return nil, ""
	}
	return found, strings.TrimSpace(rest)
}

// scanName returns the leading run of name bytes in s and its width, or a
// zero width when the run is not a name: too long, or followed by a byte a
// looser charset would have continued.
func scanName(s string) (string, int) {
	end := 0
	for end < len(s) && names.IsNameByte(s[end], end) {
		end++
	}
	if end > names.MaxName {
		return "", 0
	}
	if end < len(s) && names.IsNameContinuation(s[end]) {
		return "", 0
	}
	return s[:end], end
}
