package mentions

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseLeadingMentions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		content     string
		wantNames   []string
		wantRequest string
	}{
		{"single leading mention", "@reviewer do xyz", []string{"reviewer"}, "do xyz"},
		{"two leading mentions", "@reviewer @fixer do xyz", []string{"reviewer", "fixer"}, "do xyz"},
		{"mention with no request", "@reviewer", []string{"reviewer"}, ""},
		{"mention then comma", "@reviewer, can you look", []string{"reviewer"}, ", can you look"},
		{"mention then newline", "@reviewer\nsecond line", []string{"reviewer"}, "second line"},
		{"tab separated", "@a\t@b hi", []string{"a", "b"}, "hi"},
		{"duplicate names collapse", "@a @a hi", []string{"a"}, "hi"},
		{"duplicate non adjacent", "@a hi @a", []string{"a"}, "hi @a"},
		{"non leading is inert", "don't @reviewer do that", nil, ""},
		{"mid sentence is inert", "hey @reviewer look", nil, ""},
		{"bare at sign", "@", nil, ""},
		{"at sign then space", "@ reviewer", nil, ""},
		{"name cannot start with dash", "@-x hi", nil, ""},
		{"name cannot start with underscore", "@_x hi", nil, ""},
		{"uppercase token parses", "@Reviewer hi", []string{"Reviewer"}, "hi"},
		{"digits allowed", "@a1 hi", []string{"a1"}, "hi"},
		{"underscore and dash inside", "@a_b-c hi", []string{"a_b-c"}, "hi"},
		{"empty content", "", nil, ""},
		{"whitespace only", "   \t ", nil, ""},
		{"only mentions no request", "@a @b", []string{"a", "b"}, ""},
		{"request keeps internal newlines", "@a line1\nline2", []string{"a"}, "line1\nline2"},
		{"punctuation ends name", "@a.b hi", []string{"a"}, ".b hi"},
		{"leading whitespace then mention", "  @a hi", []string{"a"}, "hi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names, request := Parse(tc.content)
			if !reflect.DeepEqual(names, tc.wantNames) {
				t.Fatalf("names = %#v, want %#v", names, tc.wantNames)
			}
			if request != tc.wantRequest {
				t.Fatalf("request = %q, want %q", request, tc.wantRequest)
			}
		})
	}
}

func TestParseHandlesTenKiB(t *testing.T) {
	names, request := Parse("@a " + strings.Repeat("x", 10*1024))
	if !reflect.DeepEqual(names, []string{"a"}) {
		t.Fatalf("names = %#v", names)
	}
	if len(request) != 10*1024 {
		t.Fatalf("request len = %d", len(request))
	}
}
