package names

import "testing"

func TestSlug(t *testing.T) {
	ok := []string{"a", "eng", "pr-review", "pr-review-2", "AbC123", "twelvechars", "abcdefghijkl"}
	bad := map[string]string{
		"":               "empty",
		"-lead":          "leading dash",
		"1lead":          "leading digit",
		".lead":          "leading dot",
		"has space":      "space",
		"has_underscore": "underscore",
		"has.dot":        "dot",
		"thirteenchars":  "thirteen bytes",
	}
	for _, s := range ok {
		if err := Slug(s); err != nil {
			t.Errorf("Slug(%q) = %v, want nil", s, err)
		}
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false, want true", s)
		}
	}
	for s, why := range bad {
		if err := Slug(s); err == nil {
			t.Errorf("Slug(%q) = nil, want an error (%s)", s, why)
		}
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true, want false (%s)", s, why)
		}
	}
}

func TestName(t *testing.T) {
	ok := []string{"a", "alice", "bob.smith", "ci.bot", "ci..bot", "X", "abcdefghijkl"}
	bad := map[string]string{
		"":              "empty",
		"-lead":         "leading dash",
		"1lead":         "leading digit",
		".lead":         "leading dot",
		"alice.":        "trailing dot",
		"ci.bot.":       "trailing dot",
		"ci-bot":        "dash",
		"ci_bot":        "underscore",
		"ci2":           "digit",
		"twelvecharsXY": "thirteen bytes",
	}
	for _, s := range ok {
		if err := Name(s); err != nil {
			t.Errorf("Name(%q) = %v, want nil", s, err)
		}
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false, want true", s)
		}
	}
	for s, why := range bad {
		if err := Name(s); err == nil {
			t.Errorf("Name(%q) = nil, want an error (%s)", s, why)
		}
		if ValidName(s) {
			t.Errorf("ValidName(%q) = true, want false (%s)", s, why)
		}
	}
}

func TestIsNameByte(t *testing.T) {
	if !IsNameByte('a', 0) || !IsNameByte('Z', 0) {
		t.Error("letters must be name bytes at any position")
	}
	if IsNameByte('.', 0) {
		t.Error("a dot may not lead a name")
	}
	if !IsNameByte('.', 1) {
		t.Error("a dot may follow a name byte")
	}
	for _, b := range []byte{'2', '-', '_', ' '} {
		if IsNameByte(b, 1) {
			t.Errorf("%q must not be a name byte", b)
		}
	}
}

func TestIsNameContinuation(t *testing.T) {
	for _, b := range []byte{'2', '-', '_'} {
		if !IsNameContinuation(b) {
			t.Errorf("IsNameContinuation(%q) = false, want true", b)
		}
	}
	for _, b := range []byte{',', '.', ' ', '@', 'a'} {
		if IsNameContinuation(b) {
			t.Errorf("IsNameContinuation(%q) = true, want false", b)
		}
	}
}
