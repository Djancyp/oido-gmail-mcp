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

// A listing must show the recipient. Not showing it was the original defect:
// an agent could list drafts but had to ask the user who each one was for.
func TestEmailTableShowsRecipientAndID(t *testing.T) {
	id := Handle{Mailbox: "[Gmail]/Drafts", UID: 42}.String()
	out := emailTable([]EmailSummary{
		{
			ID: id, Subject: "Shipment status", From: "me@example.com",
			To: "customer@example.com", Date: "Mon, 6 Jul 2026", Seen: true, HasAttachments: true,
		},
		{
			ID: Handle{Mailbox: "INBOX", UID: 43}.String(), Subject: "Hello",
			From: "bob@example.com", To: "me@example.com", Date: "Tue, 7 Jul 2026",
		},
	})
	for _, want := range []string{
		id, "read", "unread", "Shipment status",
		"customer@example.com", "has attachments",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emailTable output missing %q:\n%s", want, out)
		}
	}
}

// The point of merge-patch: change the body without knowing, or destroying,
// anything else on the draft.
func TestMergeDraftPreservesUnmentionedFields(t *testing.T) {
	original := &OutgoingMessage{
		From:       "me@example.com",
		To:         []string{"customer@example.com"},
		Cc:         []string{"colleague@example.com"},
		Bcc:        []string{"archive@example.com"},
		Subject:    "Shipment status",
		TextBody:   "Original body.",
		IncludeBcc: true,
		Attachments: []Attachment{
			{Filename: "invoice.pdf", ContentType: "application/pdf", Data: []byte("%PDF fake")},
		},
	}
	raw, err := original.Build()
	if err != nil {
		t.Fatal(err)
	}
	pm, err := ParseMessage(raw, true)
	if err != nil {
		t.Fatal(err)
	}

	newBody := "Rewritten body."
	merged := mergeDraft(raw, pm, DraftPatch{BodyText: &newBody}, "fallback@example.com")

	if merged.TextBody != newBody {
		t.Errorf("body = %q, want %q", merged.TextBody, newBody)
	}
	if len(merged.To) != 1 || !strings.Contains(merged.To[0], "customer@example.com") {
		t.Errorf("To = %v, want the original recipient preserved", merged.To)
	}
	if len(merged.Cc) != 1 || !strings.Contains(merged.Cc[0], "colleague@example.com") {
		t.Errorf("Cc = %v, want preserved", merged.Cc)
	}
	if len(merged.Bcc) != 1 || !strings.Contains(merged.Bcc[0], "archive@example.com") {
		t.Errorf("Bcc = %v, want preserved from the draft's stored header", merged.Bcc)
	}
	if merged.Subject != "Shipment status" {
		t.Errorf("Subject = %q, want preserved", merged.Subject)
	}
	if len(merged.Attachments) != 1 || merged.Attachments[0].Filename != "invoice.pdf" {
		t.Errorf("attachments = %+v, want the original carried over", merged.Attachments)
	}
	if !merged.IncludeBcc {
		t.Error("merged draft must retain its Bcc header")
	}
}

// An explicitly supplied empty value clears the field; that is what
// distinguishes it from omission.
func TestMergeDraftEmptyValueClears(t *testing.T) {
	original := &OutgoingMessage{
		From:       "me@example.com",
		To:         []string{"customer@example.com"},
		Cc:         []string{"colleague@example.com"},
		Subject:    "Shipment status",
		TextBody:   "Body.",
		IncludeBcc: true,
	}
	raw, err := original.Build()
	if err != nil {
		t.Fatal(err)
	}
	pm, err := ParseMessage(raw, true)
	if err != nil {
		t.Fatal(err)
	}

	empty := []string{}
	merged := mergeDraft(raw, pm, DraftPatch{Cc: &empty}, "fallback@example.com")
	if len(merged.Cc) != 0 {
		t.Errorf("Cc = %v, want cleared", merged.Cc)
	}
	if len(merged.To) != 1 {
		t.Errorf("To = %v, want untouched", merged.To)
	}
}

// Rewriting the text of an HTML draft must not leave stale HTML behind for
// recipients whose client prefers it.
func TestMergeDraftTextEditDropsStaleHTML(t *testing.T) {
	original := &OutgoingMessage{
		From:     "me@example.com",
		To:       []string{"customer@example.com"},
		Subject:  "Shipment status",
		TextBody: "Old text.",
		HTMLBody: "<p>Old <b>html</b>.</p>",
	}
	raw, err := original.Build()
	if err != nil {
		t.Fatal(err)
	}
	pm, err := ParseMessage(raw, true)
	if err != nil {
		t.Fatal(err)
	}

	newText := "New text."
	merged := mergeDraft(raw, pm, DraftPatch{BodyText: &newText}, "")
	if merged.HTMLBody != "" {
		t.Errorf("stale HTML survived a text-only edit: %q", merged.HTMLBody)
	}

	// Supplying both keeps both.
	newHTML := "<p>New html.</p>"
	merged = mergeDraft(raw, pm, DraftPatch{BodyText: &newText, BodyHTML: &newHTML}, "")
	if merged.HTMLBody != newHTML {
		t.Errorf("HTML body = %q, want %q", merged.HTMLBody, newHTML)
	}
}
