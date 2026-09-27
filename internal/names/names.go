// Package names owns Fluffle's two name rules: a slug names a channel or a
// thread, and a name identifies an author. Both are ASCII, both must start
// with a letter, and both are bounded so a terminal can render them in a
// fixed-width column.
package names

import (
	"fmt"
	"regexp"
)

const (
	// MaxSlug is the byte limit on channels.name and threads.title.
	MaxSlug = 12
	// MaxName is the byte limit on an author's name.
	MaxName = 12
)

var (
	slugRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)
	nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z.]*$`)
)

// Slug reports whether s is a legal channel or thread slug: ASCII letters,
// digits, and dashes, leading letter, at most MaxSlug bytes.
func Slug(s string) error {
	if len(s) > MaxSlug {
		return fmt.Errorf("slug %q is %d bytes, max %d", s, len(s), MaxSlug)
	}
	if !slugRe.MatchString(s) {
		return fmt.Errorf("slug %q must start with a letter and hold only letters, digits, and dashes", s)
	}
	return nil
}

// Name reports whether s is a legal author name: ASCII letters and dots,
// leading letter, at most MaxName bytes. It admits no digits, dashes, or
// underscores, so an agent profile is named ci.bot and not ci-bot.
func Name(s string) error {
	if len(s) > MaxName {
		return fmt.Errorf("name %q is %d bytes, max %d", s, len(s), MaxName)
	}
	if !nameRe.MatchString(s) {
		return fmt.Errorf("name %q must start with a letter and hold only letters and dots", s)
	}
	return nil
}

// ValidSlug reports whether s is a legal slug.
func ValidSlug(s string) bool { return Slug(s) == nil }

// ValidName reports whether s is a legal name.
func ValidName(s string) bool { return Name(s) == nil }

// IsNameByte reports whether b may appear in a name at offset pos. A dot is
// legal only after the first byte, so a name never starts with one.
func IsNameByte(b byte, pos int) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
		return true
	case b == '.':
		return pos > 0
	}
	return false
}

// IsNameContinuation reports whether b would have continued a name under a
// looser charset. A mention token followed by one of these is not a name at
// all, so @alice2 does not resolve to an agent called alice.
func IsNameContinuation(b byte) bool {
	switch {
	case b >= '0' && b <= '9', b == '-', b == '_':
		return true
	}
	return false
}
