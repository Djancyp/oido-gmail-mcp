package main

import (
	"strings"
	"testing"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{"short unchanged", "hello", 10, "hello"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"truncated with ellipsis", "hello world", 8, "hello..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncate(tt.in, tt.maxLen); got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestEmailTable(t *testing.T) {
	out := emailTable([]EmailSummary{
		{UID: 42, Subject: "Invoice", From: "alice@example.com", Date: "Mon, 6 Jul 2026", Seen: true},
		{UID: 43, Subject: "Hello", From: "bob@example.com", Date: "Tue, 7 Jul 2026", Seen: false},
	})
	for _, want := range []string{"42", "read", "43", "unread", "Invoice", "alice@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("emailTable output missing %q:\n%s", want, out)
		}
	}
}
