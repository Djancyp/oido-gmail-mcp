package main

import "testing"

func TestHandleRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		mailbox string
		uid     uint32
	}{
		{"inbox", "INBOX", 1},
		{"drafts", "[Gmail]/Drafts", 1841},
		{"all mail", "[Gmail]/All Mail", 4294967295},
		{"localized label", "Δοκιμή/Παραγγελίες", 77},
		{"label with colon", "weird:label", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := Handle{Mailbox: tt.mailbox, UID: tt.uid}
			got, err := ParseHandle(in.String())
			if err != nil {
				t.Fatalf("ParseHandle(%q) failed: %v", in.String(), err)
			}
			if got != in {
				t.Errorf("round trip = %+v, want %+v", got, in)
			}
		})
	}
}

// A handle from one mailbox must never be readable as another. This is the
// defect the handle exists to prevent: a bare UID from Drafts was previously
// interpreted against INBOX.
func TestHandleDistinguishesMailboxes(t *testing.T) {
	inbox := Handle{Mailbox: "INBOX", UID: 42}.String()
	drafts := Handle{Mailbox: "[Gmail]/Drafts", UID: 42}.String()
	if inbox == drafts {
		t.Fatal("same UID in different mailboxes produced the same id")
	}
	h, err := ParseHandle(drafts)
	if err != nil {
		t.Fatal(err)
	}
	if h.Mailbox != "[Gmail]/Drafts" {
		t.Errorf("mailbox = %q, want [Gmail]/Drafts", h.Mailbox)
	}
}

func TestParseHandleRejectsJunk(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"not base64", "INBOX:42"},
		{"bare number", "42"},
		{"no separator", encodeRaw("INBOX42")},
		{"zero uid", encodeRaw("INBOX:0")},
		{"non-numeric uid", encodeRaw("INBOX:abc")},
		{"empty mailbox", encodeRaw(":42")},
		{"missing uid", encodeRaw("INBOX:")},
		{"overflows uint32", encodeRaw("INBOX:4294967296")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseHandle(tt.in); err == nil {
				t.Errorf("ParseHandle(%q) succeeded, want error", tt.in)
			}
		})
	}
}
