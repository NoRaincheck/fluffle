package mentions

import "strings"

func Parse(content string) (names []string, request string) {
	rest := content
	seen := map[string]bool{}
	for {
		trimmed := strings.TrimLeft(rest, " \t\n\r")
		if !strings.HasPrefix(trimmed, "@") {
			break
		}
		body := trimmed[1:]
		end := 0
		for end < len(body) && isNameByte(body[end], end) {
			end++
		}
		if end == 0 {
			break
		}
		name := body[:end]
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		rest = body[end:]
	}
	if len(names) == 0 {
		return nil, ""
	}
	return names, strings.TrimSpace(rest)
}

func isNameByte(b byte, pos int) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-':
		return pos > 0
	}
	return false
}
