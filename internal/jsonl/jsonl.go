package jsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type Line struct {
	Type       string         `json:"type,omitempty"`
	Seq        int64          `json:"seq,omitempty"`
	ParentSeq  int64          `json:"parent_seq,omitempty"`
	MessageSeq int64          `json:"message_seq,omitempty"`
	Role       string         `json:"role,omitempty"`
	Name       string         `json:"name"`
	AuthorType string         `json:"author_type,omitempty"`
	Content    string         `json:"content,omitempty"`
	Emoji      string         `json:"emoji,omitempty"`
	Timestamp  string         `json:"timestamp"`
	Metadata   map[string]any `json:"metadata,omitempty"`
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
	var input struct {
		Line
		Author string `json:"author"`
	}
	if err := json.Unmarshal([]byte(s), &input); err != nil {
		return Line{}, err
	}
	l := input.Line
	if l.Name == "" {
		l.Name = input.Author
	}
	if err := l.normalize(); err != nil {
		return Line{}, err
	}
	return l, nil
}

func (l *Line) normalize() error {
	if l.Type == "" {
		l.Type = "message"
	}
	switch l.Type {
	case "message":
		if l.Role == "" || l.Content == "" {
			return fmt.Errorf("role and content required")
		}
	case "reaction":
		if l.MessageSeq <= 0 || strings.TrimSpace(l.Emoji) == "" {
			return fmt.Errorf("message_seq and emoji required")
		}
	default:
		return fmt.Errorf("unknown type %q", l.Type)
	}
	if strings.TrimSpace(l.Name) == "" {
		return fmt.Errorf("name required")
	}
	if l.Metadata == nil {
		l.Metadata = map[string]any{}
	}
	if l.AuthorType == "" {
		l.AuthorType = "human"
	}
	return nil
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
