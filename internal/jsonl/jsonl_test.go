package jsonl

import "testing"

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
