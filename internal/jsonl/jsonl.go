package jsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
)

type Line struct {
	Seq        int64          `json:"seq"`
	Role       string         `json:"role"`
	Author     string         `json:"author"`
	AuthorType string         `json:"author_type"`
	Content    string         `json:"content"`
	Timestamp  string         `json:"timestamp"`
	Metadata   map[string]any `json:"metadata"`
}

func MarshalLine(l Line) (string, error) {
	if l.Metadata == nil {
		l.Metadata = map[string]any{}
	}
	b, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func ParseLine(s string) (Line, error) {
	var l Line
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		return Line{}, err
	}
	if l.Role == "" || l.Content == "" {
		return Line{}, fmt.Errorf("role and content required")
	}
	if l.Metadata == nil {
		l.Metadata = map[string]any{}
	}
	return l, nil
}

func ParseLines(data []byte) ([]Line, error) {
	var out []Line
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if line == "" {
			continue
		}
		l, err := ParseLine(line)
		if err != nil {
			return nil, fmt.Errorf("BAD_JSONL: line %d: %w", n, err)
		}
		out = append(out, l)
	}
	return out, sc.Err()
}

func EncodeLines(lines []Line) []byte {
	var buf bytes.Buffer
	for _, l := range lines {
		s, _ := MarshalLine(l)
		buf.WriteString(s)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}
