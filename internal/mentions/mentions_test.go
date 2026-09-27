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
		{"newline separated mentions", "@a\n@b hi", []string{"a", "b"}, "hi"},
		{"duplicate names collapse", "@a @a hi", []string{"a"}, "hi"},
		{"duplicate non adjacent", "@a hi @a", []string{"a"}, "hi @a"},
		{"duplicate in one run", "@a @b @a @b hi", []string{"a", "b"}, "hi"},
		{"non leading is inert", "don't @reviewer do that", nil, ""},
		{"mid sentence is inert", "hey @reviewer look", nil, ""},
		{"bare at sign", "@", nil, ""},
		{"at sign then space", "@ reviewer", nil, ""},
		{"name cannot start with dash", "@-x hi", nil, ""},
		{"name cannot start with underscore", "@_x hi", nil, ""},
		{"uppercase token parses", "@Reviewer hi", []string{"Reviewer"}, "hi"},
		{"digit leading name is inert", "@1abc hi", nil, ""},
		{"empty content", "", nil, ""},
		{"whitespace only", "   \t ", nil, ""},
		{"only mentions no request", "@a @b", []string{"a", "b"}, ""},
		{"request keeps internal newlines", "@a line1\nline2", []string{"a"}, "line1\nline2"},
		{"dot is a name byte", "@a.b hi", []string{"a.b"}, "hi"},
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

func TestParse(t *testing.T) {
	tests := []struct {
		content string
		names   []string
		request string
	}{
		{"@alice what changed?", []string{"alice"}, "what changed?"},
		{"@alice @bob ship it", []string{"alice", "bob"}, "ship it"},
		{"@alice @alice twice", []string{"alice"}, "twice"},
		{"@alice.", []string{"alice"}, ""},
		{"@alice. what changed?", []string{"alice"}, "what changed?"},
		{"@ci.bot fix it", []string{"ci.bot"}, "fix it"},
		{"@alice, ship it", []string{"alice"}, ", ship it"},
		{"hello @alice", nil, ""},
		{"@alice2 ship it", nil, ""},
		{"@alice-bot ship it", nil, ""},
		{"@2alice ship it", nil, ""},
		{"@thirteencharsabc ship it", nil, ""},
		{"", nil, ""},
		{"@", nil, ""},
	}
	for _, tt := range tests {
		gotNames, gotRequest := Parse(tt.content)
		if !reflect.DeepEqual(gotNames, tt.names) || gotRequest != tt.request {
			t.Errorf("Parse(%q) = %q, %q; want %q, %q",
				tt.content, gotNames, gotRequest, tt.names, tt.request)
		}
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
