package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func encodeRaw(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// Header injection is the security-critical case: subjects and recipient names
// routinely originate from received mail, so a CRLF must never reach the wire.
func TestBuildRejectsHeaderInjection(t *testing.T) {
	tests := []struct {
		name string
		msg  OutgoingMessage
	}{
		{
			name: "newline in subject smuggles Bcc",
			msg: OutgoingMessage{
				From:    "me@example.com",
				To:      []string{"you@example.com"},
				Subject: "Order update\r\nBcc: attacker@evil.example",
			},
		},
		{
			name: "bare LF in subject",
			msg: OutgoingMessage{
				From:    "me@example.com",
				To:      []string{"you@example.com"},
				Subject: "Order\nX-Injected: yes",
			},
		},
		{
			name: "newline in recipient",
			msg: OutgoingMessage{
				From: "me@example.com",
				To:   []string{"you@example.com\r\nBcc: attacker@evil.example"},
			},
		},
		{
			name: "newline in In-Reply-To",
			msg: OutgoingMessage{
				From:      "me@example.com",
				To:        []string{"you@example.com"},
				InReplyTo: "<a@b>\r\nX-Injected: yes",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.msg.Build()
			if err == nil {
				t.Fatalf("Build succeeded, want rejection. Output:\n%s", out)
			}
		})
	}
}

func TestBuildRequiresRecipient(t *testing.T) {
	m := OutgoingMessage{From: "me@example.com", Subject: "hi"}
	if _, err := m.Build(); err == nil {
		t.Fatal("Build with no recipients succeeded, want error")
	}
}

// Bcc must reach the SMTP envelope but never the headers — otherwise blind
// copy is not blind. Drafts are the one exception, so send_draft can recover
// the recipients later.
func TestBccIsBlindUnlessDraft(t *testing.T) {
	m := OutgoingMessage{
		From:    "me@example.com",
		To:      []string{"you@example.com"},
		Bcc:     []string{"secret@example.com"},
		Subject: "hi",
	}

	sent, err := m.Build()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sent), "secret@example.com") {
		t.Errorf("Bcc leaked into transmitted headers:\n%s", sent)
	}

	m.IncludeBcc = true
	draft, err := m.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(draft), "Bcc: secret@example.com") {
		t.Errorf("draft did not retain Bcc:\n%s", draft)
	}

	got := m.EnvelopeRecipients()
	want := []string{"you@example.com", "secret@example.com"}
	if len(got) != len(want) {
		t.Fatalf("envelope recipients = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("envelope recipient %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSubjectIsRFC2047Encoded(t *testing.T) {
	m := OutgoingMessage{
		From:    "me@example.com",
		To:      []string{"you@example.com"},
		Subject: "Παραγγελία #42 — επιβεβαίωση",
	}
	raw, err := m.Build()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Παραγγελία") {
		t.Errorf("non-ASCII subject written raw into headers:\n%s", raw)
	}
	if !strings.Contains(string(raw), "=?utf-8?") {
		t.Errorf("subject not RFC 2047 encoded:\n%s", raw)
	}

	pm, err := ParseMessage(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if pm.Subject != m.Subject {
		t.Errorf("subject round trip = %q, want %q", pm.Subject, m.Subject)
	}
}

func TestBuildParseRoundTrip(t *testing.T) {
	m := OutgoingMessage{
		From:     "Me <me@example.com>",
		To:       []string{"Ann <ann@example.com>", "bob@example.com"},
		Cc:       []string{"cc@example.com"},
		Subject:  "Shipment status",
		TextBody: "Plain body with an = sign and a lóng ünicode line.",
		HTMLBody: "<p>Rich <b>body</b></p>",
		Attachments: []Attachment{
			{Filename: "invoice.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4 fake")},
		},
	}
	raw, err := m.Build()
	if err != nil {
		t.Fatal(err)
	}

	pm, err := ParseMessage(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if pm.Subject != "Shipment status" {
		t.Errorf("subject = %q", pm.Subject)
	}
	if !strings.Contains(pm.To, "ann@example.com") || !strings.Contains(pm.To, "bob@example.com") {
		t.Errorf("To = %q, want both recipients", pm.To)
	}
	if !strings.Contains(pm.Cc, "cc@example.com") {
		t.Errorf("Cc = %q", pm.Cc)
	}
	if pm.TextBody != m.TextBody {
		t.Errorf("text body = %q, want %q", pm.TextBody, m.TextBody)
	}
	if pm.HTMLBody != m.HTMLBody {
		t.Errorf("html body = %q, want %q", pm.HTMLBody, m.HTMLBody)
	}
	if len(pm.Attachments) != 1 || pm.Attachments[0].Filename != "invoice.pdf" {
		t.Fatalf("attachments = %+v", pm.Attachments)
	}
	if got := string(pm.attachmentData["invoice.pdf"]); got != "%PDF-1.4 fake" {
		t.Errorf("attachment data = %q", got)
	}
}

// The previous implementation returned the raw body, so a multipart message
// surfaced as boundary markers and quoted-printable escapes.
func TestParseMultipartFromTheWire(t *testing.T) {
	raw := strings.ReplaceAll(`From: sender@example.com
To: me@example.com
Subject: =?UTF-8?B?zqDOsc+BzrHOs86zzrXOu86vzrE=?=
Content-Type: multipart/alternative; boundary="BOUND"

--BOUND
Content-Type: text/plain; charset=UTF-8
Content-Transfer-Encoding: quoted-printable

Hello =E2=80=94 this costs =C2=A310.
--BOUND
Content-Type: text/html; charset=UTF-8
Content-Transfer-Encoding: base64

PHA+SGVsbG8gPGI+d29ybGQ8L2I+PC9wPg==
--BOUND--
`, "\n", "\r\n")

	pm, err := ParseMessage([]byte(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	if pm.Subject != "Παραγγελία" {
		t.Errorf("subject = %q, want decoded Greek", pm.Subject)
	}
	if want := "Hello — this costs £10."; pm.TextBody != want {
		t.Errorf("text body = %q, want %q", pm.TextBody, want)
	}
	if want := "<p>Hello <b>world</b></p>"; pm.HTMLBody != want {
		t.Errorf("html body = %q, want %q", pm.HTMLBody, want)
	}
	if strings.Contains(pm.TextBody, "BOUND") || strings.Contains(pm.TextBody, "=E2") {
		t.Errorf("MIME artefacts leaked into the body: %q", pm.TextBody)
	}
}

// A caller asking for text must get readable text even when the message only
// has an HTML part.
func TestTextFallsBackToStrippedHTML(t *testing.T) {
	pm := &ParsedMessage{HTMLBody: `<style>p{color:red}</style><p>Hello <b>world</b></p><p>Second&nbsp;line</p>`}
	got := pm.Text()
	if strings.Contains(got, "<") || strings.Contains(got, "color:red") {
		t.Errorf("markup survived stripping: %q", got)
	}
	if !strings.Contains(got, "Hello world") || !strings.Contains(got, "Second line") {
		t.Errorf("text = %q, want both lines", got)
	}
}

func TestSplitAddresses(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "a@example.com", []string{"a@example.com"}},
		{"two plain", "a@example.com, b@example.com", []string{"a@example.com", "b@example.com"}},
		{
			name: "comma inside quoted display name",
			in:   `"Doe, Jane" <jane@example.com>, bob@example.com`,
			want: []string{`"Doe, Jane" <jane@example.com>`, "bob@example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitAddresses(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("splitAddresses(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("address %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHeaderOfAndStripHeader(t *testing.T) {
	raw := strings.ReplaceAll(`From: me@example.com
To: you@example.com
Bcc: secret@example.com,
 other@example.com
Subject: hi

body
`, "\n", "\r\n")

	got := headerOf([]byte(raw), "Bcc")
	if !strings.Contains(got, "secret@example.com") || !strings.Contains(got, "other@example.com") {
		t.Errorf("headerOf(Bcc) = %q, want both addresses (continuation line folded)", got)
	}

	stripped := string(stripHeader([]byte(raw), "Bcc"))
	if strings.Contains(stripped, "secret@example.com") || strings.Contains(stripped, "other@example.com") {
		t.Errorf("stripHeader left the Bcc behind:\n%s", stripped)
	}
	for _, keep := range []string{"From: me@example.com", "Subject: hi", "body"} {
		if !strings.Contains(stripped, keep) {
			t.Errorf("stripHeader removed %q:\n%s", keep, stripped)
		}
	}
}

// Attachment filenames come from received mail, so they are untrusted input.
func TestSafeAttachmentPathBlocksTraversal(t *testing.T) {
	for _, bad := range []string{
		"../../etc/passwd",
		"/etc/passwd",
		"..",
		"foo/../../../etc/passwd",
		"",
	} {
		got, err := safeAttachmentPath(bad)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(got, attachmentDir+"/") {
			t.Errorf("safeAttachmentPath(%q) = %q, escaped %q", bad, got, attachmentDir)
		}
	}

	got, err := safeAttachmentPath("invoice.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got != attachmentDir+"/invoice.pdf" {
		t.Errorf("safeAttachmentPath = %q, want %s/invoice.pdf", got, attachmentDir)
	}
}

func TestResolveOutgoingAttachmentsRejectsEscapes(t *testing.T) {
	for _, bad := range []string{"/etc/passwd", "../../../etc/passwd"} {
		if _, err := resolveOutgoingAttachments([]string{bad}); err == nil {
			t.Errorf("resolveOutgoingAttachments(%q) succeeded, want rejection", bad)
		}
	}
}
