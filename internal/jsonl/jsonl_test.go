package jsonl

import (
	"encoding/json"
	"testing"
)

func TestRoundTripPreservesFields(t *testing.T) {
	in := []Line{{Seq: 99, Role: "assistant", Name: "pi-agent", AuthorType: "agent", Content: "hi", Timestamp: "2026-09-22T00:00:00Z", Metadata: map[string]any{}}}
	data := EncodeLines(in)
	out, err := ParseLines(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Content != "hi" || out[0].Role != "assistant" {
		t.Fatalf("%+v", out)
	}
	if out[0].Seq != 99 {
		t.Fatalf("seq %+v", out)
	}
}

func TestBadJSONLNamesLine(t *testing.T) {
	_, err := ParseLines([]byte("{\"seq\":1}\nnot-json\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseLineDefaultsMissingTypeToMessage(t *testing.T) {
	got, err := ParseLine(`{"role":"user","name":"alice","content":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "message" {
		t.Fatalf("type = %q, want message", got.Type)
	}
}

func TestRoundTripPreservesParentSequence(t *testing.T) {
	in := Line{Type: "message", Seq: 9, ParentSeq: 4, Role: "assistant", Name: "pi-agent", AuthorType: "agent", Content: "reply", Timestamp: "2026-09-25T00:00:00Z", Metadata: map[string]any{}}
	encoded, err := MarshalLine(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseLine(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentSeq != 4 {
		t.Fatalf("parent_seq = %d, want 4", got.ParentSeq)
	}
}

func TestParseLineAcceptsLegacyAuthorAliasAndEmitsName(t *testing.T) {
	got, err := ParseLine(`{"role":"user","author":"legacy-agent","content":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "legacy-agent" {
		t.Fatalf("name = %q, want legacy-agent", got.Name)
	}
	encoded, err := MarshalLine(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(encoded), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["name"] != "legacy-agent" {
		t.Fatalf("name field = %#v, want legacy-agent", fields["name"])
	}
	if _, ok := fields["author"]; ok {
		t.Fatal("legacy author field emitted")
	}
}

func TestParseLineParsesReaction(t *testing.T) {
	got, err := ParseLine(`{"type":"reaction","seq":8,"parent_seq":3,"message_seq":6,"name":"bob","author_type":"human","emoji":"+1","timestamp":"2026-09-25T00:00:00Z","metadata":{"source":"test"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "reaction" || got.Seq != 8 || got.ParentSeq != 3 || got.MessageSeq != 6 {
		t.Fatalf("reaction identity = %+v", got)
	}
	if got.Name != "bob" || got.Emoji != "+1" || got.Metadata["source"] != "test" {
		t.Fatalf("reaction payload = %+v", got)
	}
}

func TestParseLineRequiresReactionFields(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"message_seq", `{"type":"reaction","role":"user","content":"ignored","name":"alice","emoji":"+1","timestamp":"2026-09-25T00:00:00Z"}`},
		{"emoji", `{"type":"reaction","role":"user","content":"ignored","name":"alice","message_seq":6,"timestamp":"2026-09-25T00:00:00Z"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseLine(tt.input); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseLineRequiresReactionName(t *testing.T) {
	if _, err := ParseLine(`{"type":"reaction","message_seq":6,"emoji":"+1","timestamp":"2026-09-25T00:00:00Z"}`); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseLineRejectsUnknownType(t *testing.T) {
	if _, err := ParseLine(`{"type":"thread","role":"user","name":"alice","content":"hello"}`); err == nil {
		t.Fatal("expected error")
	}
}
