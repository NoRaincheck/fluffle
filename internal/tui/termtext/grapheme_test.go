package termtext

import "testing"

// The format characters that join one grapheme into one glyph. Everything else
// in Cf is either invisible, a bidi control, or an annotation, and is dropped.
var joiners = []struct {
	name  string
	in    string
	want  string
	cells int
}{
	{"zero width joiner", "\U0001F468‍\U0001F469‍\U0001F467‍\U0001F466", "\U0001F468‍\U0001F469‍\U0001F467‍\U0001F466", 2},
	{"rainbow flag", "\U0001F3F3️‍\U0001F308", "\U0001F3F3️‍\U0001F308", 2},
	{"woman technologist", "\U0001F469\U0001F3FD‍\U0001F4BB", "\U0001F469\U0001F3FD‍\U0001F4BB", 2},
	{"zero width non joiner", "a\u200cb", "a\u200cb", 2},
	{"tag sequence", "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F", "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F", 2},
}

// A joiner inside a grapheme is content, not formatting: dropping it turns one
// glyph into several and doubles the cell width the rest of the TUI measures
// against, so a ZWJ sequence has to survive sanitizing unchanged.
func TestSanitizeLineKeepsGraphemeJoiners(t *testing.T) {
	for _, tc := range joiners {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeLine(tc.in); got != tc.want {
				t.Errorf("SanitizeLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if got := SanitizeBlock(tc.in); got != tc.want {
				t.Errorf("SanitizeBlock(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if got := DisplayWidth(SanitizeLine(tc.in)); got != tc.cells {
				t.Errorf("a sanitized %s measures %d cells, want %d", tc.name, got, tc.cells)
			}
		})
	}
}

// Keeping the joiners must not reopen the hole the Cf strip closed: the bidi
// controls reorder what follows them, so they still have to go.
func TestSanitizeLineStillDropsBidiControls(t *testing.T) {
	for _, r := range []rune{
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069', '\ufeff',
	} {
		in := "a" + string(r) + "b"
		if got := SanitizeLine(in); got != "ab" {
			t.Errorf("SanitizeLine(%q) = %q, want %q", in, got, "ab")
		}
	}
}

// A word joiner between two words is invisible, so keeping it must not cost the
// line a cell: the width a caller measures is still the width it draws.
func TestKeptJoinersMeasureZero(t *testing.T) {
	if got := DisplayWidth(SanitizeLine("a\u2060b")); got != 2 {
		t.Errorf("a word joiner measured %d cells, want 2", got)
	}
}

// Truncation and wrapping run after sanitizing, so a ZWJ sequence has to still
// be one grapheme at that point: a cut that lands between two emoji joined by a
// ZWJ draws half a glyph in the cell budget.
func TestTruncateDoesNotSplitAJoinedSequence(t *testing.T) {
	const family = "\U0001F468‍\U0001F469‍\U0001F467‍\U0001F466"
	for width := 1; width <= 8; width++ {
		got := Truncate(SanitizeLine(family+"x"), width, "")
		if n := DisplayWidth(got); n > width {
			t.Errorf("Truncate to %d cells returned %q at %d cells", width, got, n)
		}
		if got != "" && got != family && got != family+"x" {
			t.Errorf("Truncate to %d cells cut the sequence: %q", width, got)
		}
	}
}
