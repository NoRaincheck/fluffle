package tui

import (
	"testing"
)

func TestFormatTime(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantFn func(string) bool
	}{
		{
			name:   "empty input returns empty",
			input:  "",
			wantFn: func(s string) bool { return s == "" },
		},
		{
			name:  "RFC3339 time is formatted dynamically",
			input: "2025-01-15T14:30:00Z",
			wantFn: func(s string) bool {
				// Should produce "Jan 15 14:30", NOT the literal "Jan 02 15:04"
				if s == "Jan 02 15:04" {
					t.Errorf("formatTime returned hardcoded layout string, not formatted time")
					return false
				}
				return s == "Jan 15 14:30"
			},
		},
		{
			name:  "different timestamp produces different output",
			input: "2025-06-20T09:15:00Z",
			wantFn: func(s string) bool {
				if s == "Jan 02 15:04" {
					t.Errorf("formatTime returned hardcoded layout string")
					return false
				}
				return s == "Jun 20 09:15"
			},
		},
		{
			name:   "malformed input returns original string",
			input:  "not-a-time",
			wantFn: func(s string) bool { return s == "not-a-time" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTime(tt.input)
			if !tt.wantFn(got) {
				t.Errorf("formatTime(%q) = %q, unexpected result", tt.input, got)
			}
		})
	}
}

func TestFormatTimeIsNotHardcoded(t *testing.T) {
	// This test directly proves the bug claim is false:
	// formatTime does NOT return a hardcoded "Jan 02 15:04".
	// Different inputs must produce different outputs.
	inputs := []string{
		"2025-01-01T00:00:00Z",
		"2025-12-31T23:59:59Z",
		"2024-06-15T12:00:00Z",
	}

	results := make(map[string]bool)
	for _, in := range inputs {
		out := formatTime(in)
		if out == "Jan 02 15:04" {
			t.Errorf("formatTime(%q) returned hardcoded layout string: %q", in, out)
		}
		if results[out] {
			t.Errorf("formatTime produced same output (%q) for different inputs — not dynamic", out)
		}
		results[out] = true
	}
}
