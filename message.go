package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
)

// maxMIMEDepth bounds recursion through nested multipart bodies.
const maxMIMEDepth = 6

// Attachment is a file carried on a message.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// AttachmentInfo is attachment metadata without the bytes.
type AttachmentInfo struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
}

// OutgoingMessage is the single representation every outbound message is built
// from — send, draft, update, reply and forward all go through Build.
type OutgoingMessage struct {
	From       string
	To         []string
	Cc         []string
	Bcc        []string
	Subject    string
	TextBody   string
	HTMLBody   string
	InReplyTo  string
	References string
	MessageID  string
	Date       time.Time

	// IncludeBcc writes Bcc into the headers instead of omitting it. Set only
	// when appending to Drafts: a draft must remember its blind recipients so
	// gmail_send_draft can address them later. Never set when transmitting.
	IncludeBcc bool

	Attachments []Attachment
}

// EnvelopeRecipients returns every address the SMTP envelope must carry.
// Bcc appears here but never in the built headers — that separation is what
// makes blind copy actually blind.
func (m *OutgoingMessage) EnvelopeRecipients() []string {
	var out []string
	for _, group := range [][]string{m.To, m.Cc, m.Bcc} {
		for _, a := range group {
			if addr, err := parseAddress(a); err == nil {
				out = append(out, addr)
			}
		}
	}
	return out
}

// headerSafe rejects values that would inject extra headers. Subjects and
// recipient lists frequently originate from received mail, so this is an
// attacker-reachable trust boundary, not a formatting nicety.
func headerSafe(field, v string) (string, error) {
	if strings.ContainsAny(v, "\r\n") {
		return "", fmt.Errorf("illegal newline in %s header", field)
	}
	return strings.TrimSpace(v), nil
}

// parseAddress extracts the bare addr-spec from a possibly display-named address.
func parseAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty address")
	}
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Address, nil
	}
	if !strings.Contains(s, "@") {
		return "", fmt.Errorf("invalid email address %q", s)
	}
	return s, nil
}

// formatAddressList renders addresses for a header, RFC 2047-encoding any
// display names so non-ASCII senders don't render as mojibake.
func formatAddressList(field string, addrs []string) (string, error) {
	var parts []string
	for _, raw := range addrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, err := headerSafe(field, raw); err != nil {
			return "", err
		}
		if a, err := mail.ParseAddress(raw); err == nil {
			if a.Name != "" {
				parts = append(parts, fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", a.Name), a.Address))
				continue
			}
			parts = append(parts, a.Address)
			continue
		}
		addr, err := parseAddress(raw)
		if err != nil {
			return "", err
		}
		parts = append(parts, addr)
	}
	return strings.Join(parts, ", "), nil
}

// newMessageID generates a Message-ID so our own sent mail can be threaded against.
func newMessageID(from string) string {
	domain := "oido.local"
	if i := strings.LastIndex(from, "@"); i >= 0 && i < len(from)-1 {
		domain = from[i+1:]
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("<%d@%s>", time.Now().UnixNano(), domain)
	}
	return fmt.Sprintf("<%x@%s>", b, domain)
}

// Build renders the message as RFC 5322 bytes.
//
// Structure is chosen by content: text only -> text/plain; text+html ->
// multipart/alternative; any attachment -> multipart/mixed wrapping the above.
func (m *OutgoingMessage) Build() ([]byte, error) {
	from, err := parseAddress(m.From)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	if len(m.To) == 0 && len(m.Cc) == 0 && len(m.Bcc) == 0 {
		return nil, fmt.Errorf("at least one recipient (to, cc or bcc) is required")
	}

	toHdr, err := formatAddressList("To", m.To)
	if err != nil {
		return nil, err
	}
	ccHdr, err := formatAddressList("Cc", m.Cc)
	if err != nil {
		return nil, err
	}
	// Bcc is validated always, but emitted only for drafts (see IncludeBcc).
	bccHdr, err := formatAddressList("Bcc", m.Bcc)
	if err != nil {
		return nil, err
	}
	if !m.IncludeBcc {
		bccHdr = ""
	}
	subject, err := headerSafe("Subject", m.Subject)
	if err != nil {
		return nil, err
	}

	date := m.Date
	if date.IsZero() {
		date = time.Now()
	}
	msgID := m.MessageID
	if msgID == "" {
		msgID = newMessageID(from)
	}

	h := textproto.MIMEHeader{}
	h.Set("MIME-Version", "1.0")
	h.Set("From", from)
	if toHdr != "" {
		h.Set("To", toHdr)
	}
	if ccHdr != "" {
		h.Set("Cc", ccHdr)
	}
	if bccHdr != "" {
		h.Set("Bcc", bccHdr)
	}
	h.Set("Subject", mime.QEncoding.Encode("utf-8", subject))
	h.Set("Date", date.Format(time.RFC1123Z))
	h.Set("Message-Id", msgID)
	if m.InReplyTo != "" {
		v, err := headerSafe("In-Reply-To", m.InReplyTo)
		if err != nil {
			return nil, err
		}
		h.Set("In-Reply-To", v)
	}
	if m.References != "" {
		v, err := headerSafe("References", m.References)
		if err != nil {
			return nil, err
		}
		h.Set("References", v)
	}

	body, bodyCT, err := m.buildBody()
	if err != nil {
		return nil, err
	}
	h.Set("Content-Type", bodyCT)
	if !strings.HasPrefix(bodyCT, "multipart/") {
		h.Set("Content-Transfer-Encoding", "quoted-printable")
	}

	var buf bytes.Buffer
	// Deterministic header order keeps output stable and diffable in tests.
	for _, k := range []string{
		"MIME-Version", "From", "To", "Cc", "Bcc", "Subject", "Date",
		"Message-Id", "In-Reply-To", "References", "Content-Type", "Content-Transfer-Encoding",
	} {
		if v := h.Get(k); v != "" {
			fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
		}
	}
	buf.WriteString("\r\n")
	buf.Write(body)
	return buf.Bytes(), nil
}

// buildBody renders the body and returns it with its Content-Type header value.
func (m *OutgoingMessage) buildBody() ([]byte, string, error) {
	text := m.TextBody
	if text == "" && m.HTMLBody != "" {
		text = htmlToText(m.HTMLBody)
	}

	// Innermost: the text (and optionally html) alternative.
	var inner []byte
	var innerCT string
	switch {
	case m.HTMLBody == "":
		enc, err := qpEncode(text)
		if err != nil {
			return nil, "", err
		}
		inner, innerCT = enc, "text/plain; charset=UTF-8"
	default:
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		if err := writeTextPart(w, "text/plain; charset=UTF-8", text); err != nil {
			return nil, "", err
		}
		if err := writeTextPart(w, "text/html; charset=UTF-8", m.HTMLBody); err != nil {
			return nil, "", err
		}
		if err := w.Close(); err != nil {
			return nil, "", err
		}
		inner = buf.Bytes()
		innerCT = "multipart/alternative; boundary=" + w.Boundary()
	}

	if len(m.Attachments) == 0 {
		return inner, innerCT, nil
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	bodyHdr := textproto.MIMEHeader{"Content-Type": {innerCT}}
	if !strings.HasPrefix(innerCT, "multipart/") {
		bodyHdr.Set("Content-Transfer-Encoding", "quoted-printable")
	}
	part, err := w.CreatePart(bodyHdr)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(inner); err != nil {
		return nil, "", err
	}

	for _, att := range m.Attachments {
		if _, err := headerSafe("Content-Disposition", att.Filename); err != nil {
			return nil, "", err
		}
		ct := att.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		ah := textproto.MIMEHeader{
			"Content-Type":              {mime.FormatMediaType(ct, map[string]string{"name": att.Filename})},
			"Content-Transfer-Encoding": {"base64"},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": att.Filename})},
		}
		ap, err := w.CreatePart(ah)
		if err != nil {
			return nil, "", err
		}
		if err := writeBase64Wrapped(ap, att.Data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "multipart/mixed; boundary=" + w.Boundary(), nil
}

func writeTextPart(w *multipart.Writer, contentType, body string) error {
	p, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {contentType},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	enc, err := qpEncode(body)
	if err != nil {
		return err
	}
	_, err = p.Write(enc)
	return err
}

func qpEncode(s string) ([]byte, error) {
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	if _, err := io.WriteString(w, s); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeBase64Wrapped emits base64 in 76-character lines as MIME requires.
func writeBase64Wrapped(w io.Writer, data []byte) error {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		if _, err := io.WriteString(w, enc[:76]+"\r\n"); err != nil {
			return err
		}
		enc = enc[76:]
	}
	if enc != "" {
		if _, err := io.WriteString(w, enc+"\r\n"); err != nil {
			return err
		}
	}
	return nil
}

// ParsedMessage is a received message decoded from the wire.
type ParsedMessage struct {
	Subject    string
	From       string
	To         string
	Cc         string
	ReplyTo    string
	Date       string
	MessageID  string
	References string

	TextBody string
	HTMLBody string

	Attachments []AttachmentInfo

	// attachmentData holds bytes keyed by filename, populated only when the
	// caller asked for them.
	attachmentData map[string][]byte
}

// ParseMessage decodes raw message bytes into headers, both body alternatives
// and attachment metadata. It walks MIME properly — the previous implementation
// returned the raw body, so multipart mail surfaced as boundary markers and
// quoted-printable escapes.
func ParseMessage(raw []byte, wantAttachmentData bool) (*ParsedMessage, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to parse message: %w", err)
	}

	dec := &mime.WordDecoder{CharsetReader: charsetReader}
	hdr := func(k string) string {
		v := msg.Header.Get(k)
		if v == "" {
			return ""
		}
		if out, err := dec.DecodeHeader(v); err == nil {
			return out
		}
		return v
	}

	pm := &ParsedMessage{
		Subject:        hdr("Subject"),
		From:           hdr("From"),
		To:             hdr("To"),
		Cc:             hdr("Cc"),
		ReplyTo:        hdr("Reply-To"),
		Date:           msg.Header.Get("Date"),
		MessageID:      msg.Header.Get("Message-Id"),
		References:     msg.Header.Get("References"),
		attachmentData: map[string][]byte{},
	}
	if pm.Subject == "" {
		pm.Subject = "(no subject)"
	}

	pm.walk(
		msg.Header.Get("Content-Type"),
		msg.Header.Get("Content-Transfer-Encoding"),
		"",
		msg.Body,
		0,
		wantAttachmentData,
	)
	return pm, nil
}

// walk recursively descends a MIME tree, collecting text, html and attachments.
func (pm *ParsedMessage) walk(contentType, transferEncoding, disposition string, body io.Reader, depth int, wantData bool) {
	if depth > maxMIMEDepth {
		return
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType, params = "text/plain", map[string]string{}
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return
		}
		mr := multipart.NewReader(decodeTransferEncoding(transferEncoding, body), boundary)
		for {
			part, err := mr.NextPart()
			if err != nil {
				return
			}
			pm.walk(
				part.Header.Get("Content-Type"),
				part.Header.Get("Content-Transfer-Encoding"),
				part.Header.Get("Content-Disposition"),
				part,
				depth+1,
				wantData,
			)
		}
	}

	filename := attachmentFilename(disposition, params)
	isAttachment := filename != "" ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(disposition)), "attachment")

	data, err := io.ReadAll(decodeTransferEncoding(transferEncoding, body))
	if err != nil {
		return
	}

	if isAttachment {
		if filename == "" {
			filename = fmt.Sprintf("attachment-%d", len(pm.Attachments)+1)
		}
		pm.Attachments = append(pm.Attachments, AttachmentInfo{
			Filename:    filename,
			ContentType: mediaType,
			Size:        len(data),
		})
		if wantData {
			pm.attachmentData[filename] = data
		}
		return
	}

	text := decodeCharset(params["charset"], data)
	switch mediaType {
	case "text/html":
		if pm.HTMLBody == "" {
			pm.HTMLBody = strings.TrimSpace(text)
		}
	case "text/plain":
		if pm.TextBody == "" {
			pm.TextBody = strings.TrimSpace(text)
		}
	}
}

func attachmentFilename(disposition string, ctParams map[string]string) string {
	if disposition != "" {
		if _, params, err := mime.ParseMediaType(disposition); err == nil {
			if f := params["filename"]; f != "" {
				return f
			}
		}
	}
	return ctParams["name"]
}

// Text returns the plain-text reading of the message, falling back to a
// stripped rendering of the HTML part when there is no text/plain alternative.
func (pm *ParsedMessage) Text() string {
	if pm.TextBody != "" {
		return pm.TextBody
	}
	if pm.HTMLBody != "" {
		return htmlToText(pm.HTMLBody)
	}
	return ""
}

// decodeCharset converts common non-UTF-8 bodies. Unknown charsets are returned
// unchanged rather than mangled.
func decodeCharset(charset string, data []byte) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return string(data)
	case "iso-8859-1", "latin1", "iso8859-1", "windows-1252":
		// Latin-1 maps 1:1 onto the first 256 code points.
		runes := make([]rune, len(data))
		for i, b := range data {
			runes[i] = rune(b)
		}
		return string(runes)
	default:
		return string(data)
	}
}

func charsetReader(charset string, r io.Reader) (io.Reader, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(decodeCharset(charset, data)), nil
}

// htmlToText renders an HTML body as readable plain text. Deliberately naive:
// it exists so a caller asking for text never gets a wall of markup, not to be
// a browser.
func htmlToText(s string) string {
	s = stripElement(s, "script")
	s = stripElement(s, "style")

	var b strings.Builder
	inTag := false
	var tag strings.Builder
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
			tag.Reset()
		case r == '>' && inTag:
			inTag = false
			name := strings.ToLower(strings.TrimPrefix(strings.Fields(tag.String() + " ")[0], "/"))
			switch name {
			case "br", "p", "div", "tr", "li", "h1", "h2", "h3", "h4", "h5", "h6":
				b.WriteString("\n")
			case "td", "th":
				b.WriteString("\t")
			}
		case !inTag:
			b.WriteRune(r)
		default:
			tag.WriteRune(r)
		}
	}

	out := html.UnescapeString(b.String())
	lines := strings.Split(out, "\n")
	var kept []string
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(strings.ReplaceAll(l, " ", " "), " \t")
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		kept = append(kept, l)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func stripElement(s, tag string) string {
	lower := strings.ToLower(s)
	open, close := "<"+tag, "</"+tag
	for {
		i := strings.Index(lower, open)
		if i < 0 {
			return s
		}
		j := strings.Index(lower[i:], close)
		if j < 0 {
			return s[:i]
		}
		end := i + j
		if k := strings.Index(lower[end:], ">"); k >= 0 {
			end += k + 1
		}
		s = s[:i] + s[end:]
		lower = strings.ToLower(s)
	}
}
