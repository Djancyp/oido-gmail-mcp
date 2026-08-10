package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"mime/quotedprintable"
	"net/smtp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/responses"
)

// Canonical Gmail mailbox names, used as fallbacks when the server does not
// advertise the RFC 6154 special-use attribute (which is language-independent).
const (
	fallbackDrafts  = "[Gmail]/Drafts"
	fallbackTrash   = "[Gmail]/Trash"
	fallbackAllMail = "[Gmail]/All Mail"
)

// attachmentDir is where downloaded attachments are written, relative to the
// process working directory. oido-core points that directory at the tenant's
// isolated workspace, so tenant separation is the host's boundary, not a
// setting this plugin exposes — see docs/adr/001-message-handles.md context.
const attachmentDir = "attachments"

// maxAttachmentBytes caps total outbound attachment size (Gmail's own limit).
const maxAttachmentBytes = 25 << 20

// maxInlineAttachment bounds what read may inline rather than write to disk.
const maxInlineAttachment = 32 << 10

// EmailSettings holds configuration for email access.
type EmailSettings struct {
	Email       string
	AccessToken string // OAuth2 access token (XOAUTH2)
	IMAPHost    string
	IMAPPort    int
	SMTPHost    string
	SMTPPort    int

	// The three permission axes, drawn around "does this leave the building?".
	AllowRead     bool // inspect messages, labels, attachments
	AllowOrganize bool // flags, labels, location, and draft create/update/discard
	AllowSend     bool // transmit to a recipient: irreversible, externally visible
}

// DefaultEmailSettings returns settings with reversible operations enabled and
// transmission disabled.
func DefaultEmailSettings() *EmailSettings {
	return &EmailSettings{
		AllowRead:     true,
		AllowOrganize: true,
		AllowSend:     false,
	}
}

// loadDotEnv loads a .env file sitting beside the executable, if present.
func loadDotEnv() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	envPath := filepath.Join(filepath.Dir(exe), ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if key != "" && os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
	log.Printf("Loaded .env from %s", envPath)
}

// envBool reads a boolean setting, leaving dst untouched when unset or invalid.
func envBool(key string, dst *bool) {
	if val := os.Getenv(key); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			*dst = b
		}
	}
}

// parseEmailSettings reads environment variables to configure email access.
func parseEmailSettings() (*EmailSettings, error) {
	loadDotEnv()
	settings := DefaultEmailSettings()

	// OAuth-only: the access token is injected by oido-core after the user connects
	// their Google account.
	settings.AccessToken = os.Getenv("GOOGLE_ACCESS_TOKEN")
	settings.Email = os.Getenv("GOOGLE_OAUTH_EMAIL")
	if settings.Email == "" && settings.AccessToken != "" {
		if em, err := fetchGoogleEmail(settings.AccessToken); err == nil {
			settings.Email = em
		} else {
			log.Printf("Warning: could not resolve account email from OAuth token: %v", err)
		}
	}

	settings.IMAPHost = os.Getenv("GMAIL_IMAP_HOST")
	if settings.IMAPHost == "" {
		settings.IMAPHost = "imap.gmail.com"
	}
	settings.IMAPPort = 993
	if s := os.Getenv("GMAIL_IMAP_PORT"); s != "" {
		port, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("invalid GMAIL_IMAP_PORT: %w", err)
		}
		settings.IMAPPort = port
	}

	settings.SMTPHost = os.Getenv("GMAIL_SMTP_HOST")
	if settings.SMTPHost == "" {
		settings.SMTPHost = "smtp.gmail.com"
	}
	settings.SMTPPort = 587
	if s := os.Getenv("GMAIL_SMTP_PORT"); s != "" {
		port, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("invalid GMAIL_SMTP_PORT: %w", err)
		}
		settings.SMTPPort = port
	}

	// GMAIL_ALLOW_RECEIVE is the pre-existing name for the read axis; honour it
	// so tenants configured before the split keep working.
	envBool("GMAIL_ALLOW_RECEIVE", &settings.AllowRead)
	envBool("GMAIL_ALLOW_READ", &settings.AllowRead)
	envBool("GMAIL_ALLOW_ORGANIZE", &settings.AllowOrganize)
	envBool("GMAIL_ALLOW_SEND", &settings.AllowSend)

	if settings.AccessToken == "" {
		log.Println("Warning: Gmail not connected. Connect your Google account in the extension settings. Tools will return errors until connected.")
	}

	return settings, nil
}

// EmailSummary is a message as it appears in a listing.
type EmailSummary struct {
	ID             string `json:"id"`
	Subject        string `json:"subject"`
	From           string `json:"from"`
	To             string `json:"to"`
	Date           string `json:"date"`
	Seen           bool   `json:"seen"`
	Starred        bool   `json:"starred"`
	HasAttachments bool   `json:"has_attachments"`
}

// EmailDetail is a fully read message.
type EmailDetail struct {
	ID          string           `json:"id"`
	Subject     string           `json:"subject"`
	From        string           `json:"from"`
	To          string           `json:"to"`
	Cc          string           `json:"cc"`
	ReplyTo     string           `json:"reply_to"`
	Date        string           `json:"date"`
	MessageID   string           `json:"message_id"`
	BodyText    string           `json:"body_text,omitempty"`
	BodyHTML    string           `json:"body_html,omitempty"`
	Attachments []AttachmentInfo `json:"attachments,omitempty"`
}

// GmailClient manages IMAP and SMTP connections.
type GmailClient struct {
	settings *EmailSettings

	mu      sync.Mutex
	special map[string]string // special-use attribute -> resolved mailbox name
}

// NewGmailClient creates a new Gmail client from environment variables.
func NewGmailClient() (*GmailClient, error) {
	settings, err := parseEmailSettings()
	if err != nil {
		return nil, err
	}
	return &GmailClient{settings: settings, special: map[string]string{}}, nil
}

// TestConnection tests the IMAP connection via OAuth, logging the result.
func (c *GmailClient) TestConnection() {
	if c.settings.AccessToken == "" {
		log.Printf("IMAP connection SKIPPED - Gmail not connected (no OAuth token)")
		return
	}
	log.Printf("Testing IMAP connection to %s:%d...", c.settings.IMAPHost, c.settings.IMAPPort)
	imapClient, err := c.getIMAPClient()
	if err != nil {
		log.Printf("IMAP connection FAILED: %v", err)
		log.Printf("Reconnect your Google account and ensure the https://mail.google.com/ scope was granted.")
		return
	}
	imapClient.Logout()
	log.Printf("IMAP connection OK - authentication successful")
}

// getIMAPClient connects to the IMAP server and returns an authenticated client.
func (c *GmailClient) getIMAPClient() (*client.Client, error) {
	if c.settings.AccessToken == "" {
		return nil, fmt.Errorf("Gmail not connected: connect your Google account in the extension settings")
	}
	if c.settings.Email == "" {
		return nil, fmt.Errorf("Gmail OAuth token present but the account email could not be resolved — reconnect your Google account (the token may be expired or missing the email scope)")
	}

	addr := fmt.Sprintf("%s:%d", c.settings.IMAPHost, c.settings.IMAPPort)
	imapClient, err := client.DialTLS(addr, &tls.Config{ServerName: c.settings.IMAPHost})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
	}
	if err := imapClient.Authenticate(&imapXOAuth2{email: c.settings.Email, token: c.settings.AccessToken}); err != nil {
		imapClient.Logout()
		return nil, fmt.Errorf("IMAP XOAUTH2 auth failed (reconnect Google; ensure the mail scope was granted): %w", err)
	}
	return imapClient, nil
}

// ---------------------------------------------------------------------------
// Permission axes
// ---------------------------------------------------------------------------

func (c *GmailClient) requireRead() error {
	if !c.settings.AllowRead {
		return fmt.Errorf("blocked: reading this mailbox is not allowed (enable with GMAIL_ALLOW_READ=true)")
	}
	return nil
}

func (c *GmailClient) requireOrganize() error {
	if !c.settings.AllowOrganize {
		return fmt.Errorf("blocked: changing this mailbox is not allowed (enable with GMAIL_ALLOW_ORGANIZE=true)")
	}
	return nil
}

func (c *GmailClient) requireSend() error {
	if !c.settings.AllowSend {
		return fmt.Errorf("blocked: sending email is not allowed (enable with GMAIL_ALLOW_SEND=true). Saving a draft for a human to review and send does not require this permission")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Mailbox resolution
// ---------------------------------------------------------------------------

// resolveSpecial finds a mailbox by its RFC 6154 special-use attribute, which
// is language-independent, falling back to the canonical English name.
func (c *GmailClient) resolveSpecial(imapClient *client.Client, attr, fallback string) (string, error) {
	c.mu.Lock()
	if name, ok := c.special[attr]; ok {
		c.mu.Unlock()
		return name, nil
	}
	c.mu.Unlock()

	mailboxes := make(chan *imap.MailboxInfo, 100)
	done := make(chan error, 1)
	go func() { done <- imapClient.List("", "*", mailboxes) }()

	var names []string
	var found string
	for mbox := range mailboxes {
		names = append(names, mbox.Name)
		for _, a := range mbox.Attributes {
			if a == attr && found == "" {
				found = mbox.Name
			}
		}
	}
	if err := <-done; err != nil {
		return "", fmt.Errorf("failed to list mailboxes: %w", err)
	}
	if found == "" {
		for _, n := range names {
			if n == fallback {
				found = n
				break
			}
		}
	}
	if found == "" {
		return "", fmt.Errorf("no %s mailbox exposed via IMAP; enable it under Gmail Settings > Labels. Available mailboxes: %s",
			strings.TrimPrefix(attr, "\\"), strings.Join(names, ", "))
	}

	c.mu.Lock()
	c.special[attr] = found
	c.mu.Unlock()
	return found, nil
}

func (c *GmailClient) draftsMailbox(ic *client.Client) (string, error) {
	return c.resolveSpecial(ic, imap.DraftsAttr, fallbackDrafts)
}

func (c *GmailClient) trashMailbox(ic *client.Client) (string, error) {
	return c.resolveSpecial(ic, imap.TrashAttr, fallbackTrash)
}

func (c *GmailClient) allMailMailbox(ic *client.Client) (string, error) {
	return c.resolveSpecial(ic, imap.AllAttr, fallbackAllMail)
}

// searchMailbox picks the mailbox a search runs against. Gmail's X-GM-RAW is
// scoped to the selected mailbox, and handles must name the mailbox they came
// from — so a draft search has to run in Drafts, not All Mail, or the resulting
// handles would be unusable for editing.
func (c *GmailClient) searchMailbox(ic *client.Client, query string) (string, error) {
	q := strings.ToLower(query)
	switch {
	case strings.Contains(q, "in:drafts"), strings.Contains(q, "is:draft"), strings.Contains(q, "label:drafts"):
		return c.draftsMailbox(ic)
	case strings.Contains(q, "in:trash"), strings.Contains(q, "label:trash"):
		return c.trashMailbox(ic)
	default:
		return c.allMailMailbox(ic)
	}
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// Search returns message summaries matching a Gmail search query.
func (c *GmailClient) Search(ctx context.Context, query string, count int) ([]EmailSummary, error) {
	if err := c.requireRead(); err != nil {
		return nil, err
	}
	if count <= 0 {
		count = 20
	}
	if strings.TrimSpace(query) == "" {
		query = "in:inbox"
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	mailbox, err := c.searchMailbox(imapClient, query)
	if err != nil {
		return nil, err
	}
	mbox, err := imapClient.Select(mailbox, true)
	if err != nil {
		return nil, fmt.Errorf("failed to select %s: %w", mailbox, err)
	}
	if mbox.Messages == 0 {
		return []EmailSummary{}, nil
	}

	seqnums, err := rawGmailSearch(imapClient, query)
	if err != nil {
		// Non-Gmail IMAP servers have no X-GM-RAW; fall back to a subject search.
		criteria := imap.NewSearchCriteria()
		criteria.Header.Add("Subject", query)
		seqnums, err = imapClient.Search(criteria)
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
	}
	if len(seqnums) == 0 {
		return []EmailSummary{}, nil
	}
	if len(seqnums) > count {
		seqnums = seqnums[len(seqnums)-count:]
	}

	return c.summarize(imapClient, mailbox, seqnums, count)
}

// summarize fetches headers, flags and body structure for the given sequence
// numbers in one round trip and renders them as summaries.
func (c *GmailClient) summarize(imapClient *client.Client, mailbox string, seqnums []uint32, count int) ([]EmailSummary, error) {
	seqset := new(imap.SeqSet)
	for _, n := range seqnums {
		seqset.AddNum(n)
	}

	headerSection := &imap.BodySectionName{
		Peek:         true,
		BodyPartName: imap.BodyPartName{Specifier: imap.HeaderSpecifier},
	}
	items := []imap.FetchItem{
		imap.FetchUid,
		imap.FetchFlags,
		imap.FetchBodyStructure,
		headerSection.FetchItem(),
	}

	messages := make(chan *imap.Message, len(seqnums))
	go func() { _ = imapClient.Fetch(seqset, items, messages) }()

	var summaries []EmailSummary
	for msg := range messages {
		if len(summaries) >= count {
			continue // drain the channel so the fetch goroutine can finish
		}
		s := EmailSummary{ID: Handle{Mailbox: mailbox, UID: msg.Uid}.String()}
		for _, flag := range msg.Flags {
			switch flag {
			case imap.SeenFlag:
				s.Seen = true
			case imap.FlaggedFlag:
				s.Starred = true
			}
		}
		s.HasAttachments = bodyStructureHasAttachment(msg.BodyStructure)

		if r := msg.GetBody(headerSection); r != nil {
			headerData, _ := io.ReadAll(r)
			if pm, err := ParseMessage(headerData, false); err == nil {
				s.Subject, s.From, s.To, s.Date = pm.Subject, pm.From, pm.To, pm.Date
			}
		}
		if s.Subject == "" {
			s.Subject = "(no subject)"
		}
		summaries = append(summaries, s)
	}
	return summaries, nil
}

// bodyStructureHasAttachment reports whether a message carries a named or
// explicitly attached part, without fetching the body.
func bodyStructureHasAttachment(bs *imap.BodyStructure) bool {
	if bs == nil {
		return false
	}
	if strings.EqualFold(bs.Disposition, "attachment") {
		return true
	}
	if bs.Params["name"] != "" || bs.DispositionParams["filename"] != "" {
		return true
	}
	for _, part := range bs.Parts {
		if bodyStructureHasAttachment(part) {
			return true
		}
	}
	return false
}

// Read fetches one message. format selects which body alternatives are
// returned: "text" (default), "html", or "both".
func (c *GmailClient) Read(ctx context.Context, h Handle, format string) (*EmailDetail, error) {
	if err := c.requireRead(); err != nil {
		return nil, err
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	raw, err := c.fetchRaw(imapClient, h)
	if err != nil {
		return nil, err
	}
	pm, err := ParseMessage(raw, false)
	if err != nil {
		return nil, err
	}

	d := &EmailDetail{
		ID:          h.String(),
		Subject:     pm.Subject,
		From:        pm.From,
		To:          pm.To,
		Cc:          pm.Cc,
		ReplyTo:     pm.ReplyTo,
		Date:        pm.Date,
		MessageID:   pm.MessageID,
		Attachments: pm.Attachments,
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "html":
		d.BodyHTML = pm.HTMLBody
		if d.BodyHTML == "" {
			d.BodyText = pm.Text()
		}
	case "both":
		d.BodyText = pm.Text()
		d.BodyHTML = pm.HTMLBody
	default:
		d.BodyText = pm.Text()
	}
	return d, nil
}

// fetchRaw retrieves the full bytes of the message a handle names, selecting
// the mailbox the handle carries. A handle that no longer resolves fails rather
// than falling back to another mailbox.
func (c *GmailClient) fetchRaw(imapClient *client.Client, h Handle) ([]byte, error) {
	if _, err := imapClient.Select(h.Mailbox, true); err != nil {
		return nil, fmt.Errorf("failed to select %s: %w", h.Mailbox, err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(h.UID)
	section := &imap.BodySectionName{Peek: true}

	messages := make(chan *imap.Message, 1)
	go func() { _ = imapClient.UidFetch(seqset, []imap.FetchItem{section.FetchItem()}, messages) }()

	msg := <-messages
	if msg == nil {
		return nil, fmt.Errorf("message not found in %s — the id is stale (the message was moved, edited or deleted); search again to get a current id", h.Mailbox)
	}
	r := msg.GetBody(section)
	if r == nil {
		return nil, fmt.Errorf("failed to read message body in %s", h.Mailbox)
	}
	return io.ReadAll(r)
}

// rawGmailSearch runs SEARCH X-GM-RAW <query>, giving full Gmail search syntax.
func rawGmailSearch(imapClient *client.Client, query string) ([]uint32, error) {
	cmd := &imap.Command{
		Name:      "SEARCH",
		Arguments: []interface{}{imap.RawString("X-GM-RAW"), query},
	}
	res := new(responses.Search)
	status, err := imapClient.Execute(cmd, res)
	if err != nil {
		return nil, err
	}
	if err := status.Err(); err != nil {
		return nil, err
	}
	return res.Ids, nil
}

// ---------------------------------------------------------------------------
// Sending
// ---------------------------------------------------------------------------

// Send transmits a message via SMTP.
func (c *GmailClient) Send(ctx context.Context, m *OutgoingMessage) error {
	if err := c.requireSend(); err != nil {
		return err
	}
	if m.From == "" {
		m.From = c.settings.Email
	}
	m.IncludeBcc = false // never disclose blind recipients in transmitted headers

	raw, err := m.Build()
	if err != nil {
		return err
	}
	return c.sendRaw(m.From, m.EnvelopeRecipients(), raw)
}

// sendRaw hands pre-built bytes to SMTP with an explicit envelope recipient
// list, which is what keeps Bcc blind: recipients live in the envelope, not
// necessarily in the headers.
func (c *GmailClient) sendRaw(from string, recipients []string, msg []byte) error {
	if from == "" {
		from = c.settings.Email
	}
	if c.settings.AccessToken == "" || c.settings.Email == "" {
		return fmt.Errorf("Gmail not connected: connect your Google account in the extension settings")
	}
	if len(recipients) == 0 {
		return fmt.Errorf("no recipients to send to")
	}

	addr := fmt.Sprintf("%s:%d", c.settings.SMTPHost, c.settings.SMTPPort)
	var auth smtp.Auth = &smtpXOAuth2{email: c.settings.Email, token: c.settings.AccessToken}

	if c.settings.SMTPPort == 465 {
		return c.sendSMTPS(addr, auth, from, recipients, msg)
	}

	smtpClient, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP server: %w", err)
	}
	defer smtpClient.Close()

	if err := smtpClient.StartTLS(&tls.Config{ServerName: c.settings.SMTPHost}); err != nil {
		return fmt.Errorf("STARTTLS failed: %w", err)
	}
	if err := smtpClient.Auth(auth); err != nil {
		return fmt.Errorf("SMTP auth failed: %w", err)
	}
	return writeSMTP(smtpClient, from, recipients, msg)
}

// sendSMTPS sends using implicit TLS (SMTPS on port 465).
func (c *GmailClient) sendSMTPS(addr string, auth smtp.Auth, from string, recipients []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: c.settings.SMTPHost})
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP server: %w", err)
	}
	defer conn.Close()

	smtpClient, err := smtp.NewClient(conn, c.settings.SMTPHost)
	if err != nil {
		return fmt.Errorf("failed to create SMTP client: %w", err)
	}
	defer smtpClient.Close()

	if err := smtpClient.Auth(auth); err != nil {
		return fmt.Errorf("SMTP auth failed: %w", err)
	}
	return writeSMTP(smtpClient, from, recipients, msg)
}

func writeSMTP(smtpClient *smtp.Client, from string, recipients []string, msg []byte) error {
	if err := smtpClient.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM failed: %w", err)
	}
	for _, rcpt := range recipients {
		if err := smtpClient.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO %s failed: %w", rcpt, err)
		}
	}
	w, err := smtpClient.Data()
	if err != nil {
		return fmt.Errorf("DATA failed: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("failed to finish message: %w", err)
	}
	return smtpClient.Quit()
}

// ---------------------------------------------------------------------------
// Drafts
// ---------------------------------------------------------------------------

// SaveDraft appends a new draft and returns its handle.
func (c *GmailClient) SaveDraft(ctx context.Context, m *OutgoingMessage) (Handle, error) {
	if err := c.requireOrganize(); err != nil {
		return Handle{}, err
	}
	if m.From == "" {
		m.From = c.settings.Email
	}
	m.IncludeBcc = true // a draft must remember its blind recipients

	raw, err := m.Build()
	if err != nil {
		return Handle{}, err
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return Handle{}, err
	}
	defer imapClient.Logout()

	drafts, err := c.draftsMailbox(imapClient)
	if err != nil {
		return Handle{}, err
	}
	return c.appendDraft(imapClient, drafts, raw)
}

// appendDraft APPENDs to Drafts and resolves the UID of what it wrote. Gmail
// does not return APPENDUID, so the new message is located by its Message-ID.
func (c *GmailClient) appendDraft(imapClient *client.Client, drafts string, raw []byte) (Handle, error) {
	flags := []string{imap.DraftFlag, imap.SeenFlag}
	if err := imapClient.Append(drafts, flags, time.Now(), bytes.NewReader(raw)); err != nil {
		return Handle{}, fmt.Errorf("failed to save draft to %s: %w", drafts, err)
	}

	msgID := messageIDOf(raw)
	if msgID == "" {
		return Handle{Mailbox: drafts}, nil
	}
	if _, err := imapClient.Select(drafts, false); err != nil {
		return Handle{}, fmt.Errorf("failed to select %s: %w", drafts, err)
	}
	criteria := imap.NewSearchCriteria()
	criteria.Header.Add("Message-Id", msgID)
	uids, err := imapClient.UidSearch(criteria)
	if err != nil || len(uids) == 0 {
		// The draft is saved; we just cannot name it. Better to say so than to
		// return a handle that points at the wrong message.
		return Handle{Mailbox: drafts}, nil
	}
	return Handle{Mailbox: drafts, UID: uids[len(uids)-1]}, nil
}

// messageIDOf extracts the Message-Id header from raw message bytes.
func messageIDOf(raw []byte) string {
	pm, err := ParseMessage(raw, false)
	if err != nil {
		return ""
	}
	return pm.MessageID
}

// DraftPatch describes a partial update to a draft. A nil field is left
// untouched; a non-nil empty value clears the field. That distinction is why
// these are pointers — it lets a caller change the body without knowing, or
// accidentally erasing, the recipient.
type DraftPatch struct {
	From     *string
	To       *[]string
	Cc       *[]string
	Bcc      *[]string
	Subject  *string
	BodyText *string
	BodyHTML *string
}

// UpdateDraft rewrites a draft with the supplied fields overlaid on the
// existing one. IMAP cannot edit in place, so this appends the merged message
// and then discards the original — in that order, so a failure between the two
// leaves a visible duplicate rather than destroying the user's work. The
// returned handle is new; the one passed in is dead.
func (c *GmailClient) UpdateDraft(ctx context.Context, h Handle, patch DraftPatch) (Handle, error) {
	if err := c.requireOrganize(); err != nil {
		return Handle{}, err
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return Handle{}, err
	}
	defer imapClient.Logout()

	drafts, err := c.draftsMailbox(imapClient)
	if err != nil {
		return Handle{}, err
	}
	if h.Mailbox != drafts {
		return Handle{}, fmt.Errorf("only drafts can be edited; this id points at %s. Received and sent mail is an immutable record", h.Mailbox)
	}

	raw, err := c.fetchRaw(imapClient, h)
	if err != nil {
		return Handle{}, err
	}
	pm, err := ParseMessage(raw, true)
	if err != nil {
		return Handle{}, err
	}

	merged := mergeDraft(raw, pm, patch, c.settings.Email)

	newRaw, err := merged.Build()
	if err != nil {
		return Handle{}, err
	}

	newHandle, err := c.appendDraft(imapClient, drafts, newRaw)
	if err != nil {
		return Handle{}, err
	}
	if err := c.expunge(imapClient, h); err != nil {
		return newHandle, fmt.Errorf("draft updated (new id returned) but the old copy could not be removed — you now have two drafts: %w", err)
	}
	return newHandle, nil
}

// mergeDraft overlays a patch onto an existing draft. Fields the patch does not
// mention are carried over from the original — including attachments and the
// Bcc list, neither of which a caller can see well enough to re-supply.
func mergeDraft(raw []byte, pm *ParsedMessage, patch DraftPatch, defaultFrom string) *OutgoingMessage {
	merged := &OutgoingMessage{
		From:       firstNonEmpty(derefStr(patch.From), pm.From, defaultFrom),
		To:         derefList(patch.To, splitAddresses(pm.To)),
		Cc:         derefList(patch.Cc, splitAddresses(pm.Cc)),
		Bcc:        derefList(patch.Bcc, splitAddresses(headerOf(raw, "Bcc"))),
		Subject:    derefOr(patch.Subject, pm.Subject),
		TextBody:   derefOr(patch.BodyText, pm.TextBody),
		HTMLBody:   derefOr(patch.BodyHTML, pm.HTMLBody),
		IncludeBcc: true,
	}
	// Rewriting the text of an HTML draft would otherwise leave the old HTML in
	// place, and recipients preferring HTML would still see the stale version.
	if patch.BodyText != nil && patch.BodyHTML == nil && pm.HTMLBody != "" {
		merged.HTMLBody = ""
	}
	for _, att := range pm.Attachments {
		if data, ok := pm.attachmentData[att.Filename]; ok {
			merged.Attachments = append(merged.Attachments, Attachment{
				Filename: att.Filename, ContentType: att.ContentType, Data: data,
			})
		}
	}
	// ParseMessage substitutes a placeholder for an absent subject; don't
	// persist that placeholder as if the user had typed it.
	if merged.Subject == "(no subject)" {
		merged.Subject = ""
	}
	return merged
}

// DeleteDraft permanently discards a draft.
func (c *GmailClient) DeleteDraft(ctx context.Context, h Handle) error {
	if err := c.requireOrganize(); err != nil {
		return err
	}
	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	drafts, err := c.draftsMailbox(imapClient)
	if err != nil {
		return err
	}
	if h.Mailbox != drafts {
		return fmt.Errorf("this id points at %s, not drafts; use gmail_trash to remove a non-draft message", h.Mailbox)
	}
	return c.expunge(imapClient, h)
}

// SendDraft transmits an existing draft and removes it from Drafts. Gmail files
// the transmitted copy into Sent automatically, but the Drafts copy is ours to
// clean up.
func (c *GmailClient) SendDraft(ctx context.Context, h Handle) error {
	if err := c.requireSend(); err != nil {
		return err
	}
	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	drafts, err := c.draftsMailbox(imapClient)
	if err != nil {
		return err
	}
	if h.Mailbox != drafts {
		return fmt.Errorf("only drafts can be sent with this tool; this id points at %s", h.Mailbox)
	}

	raw, err := c.fetchRaw(imapClient, h)
	if err != nil {
		return err
	}
	pm, err := ParseMessage(raw, false)
	if err != nil {
		return err
	}

	var recipients []string
	for _, list := range []string{pm.To, pm.Cc, headerOf(raw, "Bcc")} {
		for _, a := range splitAddresses(list) {
			if addr, err := parseAddress(a); err == nil {
				recipients = append(recipients, addr)
			}
		}
	}
	if len(recipients) == 0 {
		return fmt.Errorf("draft has no recipients; set them with gmail_update_draft before sending")
	}

	from := c.settings.Email
	if addr, err := parseAddress(pm.From); err == nil {
		from = addr
	}
	// Strip Bcc before transmission — it is stored in the draft but must not
	// travel on the wire.
	if err := c.sendRaw(from, recipients, stripHeader(raw, "Bcc")); err != nil {
		return err
	}
	if err := c.expunge(imapClient, h); err != nil {
		return fmt.Errorf("message sent, but the draft could not be removed: %w", err)
	}
	return nil
}

// expunge flags a message deleted and removes it from its mailbox.
func (c *GmailClient) expunge(imapClient *client.Client, h Handle) error {
	if _, err := imapClient.Select(h.Mailbox, false); err != nil {
		return fmt.Errorf("failed to select %s: %w", h.Mailbox, err)
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(h.UID)
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	if err := imapClient.UidStore(seqset, item, []interface{}{imap.DeletedFlag}, nil); err != nil {
		return fmt.Errorf("failed to mark message deleted: %w", err)
	}
	if err := imapClient.Expunge(nil); err != nil {
		return fmt.Errorf("failed to expunge: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Reply / forward
// ---------------------------------------------------------------------------

// buildReply constructs the reply to a message, optionally addressing everyone
// on the original.
func (c *GmailClient) buildReply(pm *ParsedMessage, from, bodyText, bodyHTML string, replyAll bool) (*OutgoingMessage, error) {
	replyTo := pm.ReplyTo
	if replyTo == "" {
		replyTo = pm.From
	}
	to := splitAddresses(replyTo)
	if len(to) == 0 {
		return nil, fmt.Errorf("could not determine a reply address from the original message")
	}

	var cc []string
	if replyAll {
		self := strings.ToLower(c.settings.Email)
		for _, a := range append(splitAddresses(pm.To), splitAddresses(pm.Cc)...) {
			addr, err := parseAddress(a)
			if err != nil || strings.EqualFold(addr, self) {
				continue
			}
			if containsAddress(to, addr) || containsAddress(cc, addr) {
				continue
			}
			cc = append(cc, addr)
		}
	}

	subject := pm.Subject
	if subject == "(no subject)" {
		subject = ""
	}
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	references := pm.References
	if pm.MessageID != "" {
		if references != "" {
			references += " " + pm.MessageID
		} else {
			references = pm.MessageID
		}
	}

	return &OutgoingMessage{
		From:       from,
		To:         to,
		Cc:         cc,
		Subject:    subject,
		TextBody:   bodyText,
		HTMLBody:   bodyHTML,
		InReplyTo:  pm.MessageID,
		References: references,
	}, nil
}

// Reply sends a reply to the message a handle names.
func (c *GmailClient) Reply(ctx context.Context, h Handle, from, bodyText, bodyHTML string, replyAll bool) error {
	if err := c.requireSend(); err != nil {
		return err
	}
	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	raw, err := c.fetchRaw(imapClient, h)
	imapClient.Logout()
	if err != nil {
		return err
	}
	pm, err := ParseMessage(raw, false)
	if err != nil {
		return err
	}
	if from == "" {
		from = c.settings.Email
	}
	m, err := c.buildReply(pm, from, bodyText, bodyHTML, replyAll)
	if err != nil {
		return err
	}
	return c.Send(ctx, m)
}

// DraftReply saves a reply as a draft instead of sending it — the safe half of
// the workflow, available without send permission.
func (c *GmailClient) DraftReply(ctx context.Context, h Handle, from, bodyText, bodyHTML string, replyAll bool) (Handle, error) {
	if err := c.requireOrganize(); err != nil {
		return Handle{}, err
	}
	imapClient, err := c.getIMAPClient()
	if err != nil {
		return Handle{}, err
	}
	raw, err := c.fetchRaw(imapClient, h)
	imapClient.Logout()
	if err != nil {
		return Handle{}, err
	}
	pm, err := ParseMessage(raw, false)
	if err != nil {
		return Handle{}, err
	}
	if from == "" {
		from = c.settings.Email
	}
	m, err := c.buildReply(pm, from, bodyText, bodyHTML, replyAll)
	if err != nil {
		return Handle{}, err
	}
	return c.SaveDraft(ctx, m)
}

// Forward forwards a message, carrying its attachments along.
func (c *GmailClient) Forward(ctx context.Context, h Handle, from string, to []string, additionalBody string) error {
	if err := c.requireSend(); err != nil {
		return err
	}
	if len(to) == 0 {
		return fmt.Errorf("at least one recipient is required")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	raw, err := c.fetchRaw(imapClient, h)
	imapClient.Logout()
	if err != nil {
		return err
	}
	pm, err := ParseMessage(raw, true)
	if err != nil {
		return err
	}
	if from == "" {
		from = c.settings.Email
	}

	subject := pm.Subject
	if subject == "(no subject)" {
		subject = ""
	}
	if !strings.HasPrefix(strings.ToLower(subject), "fwd:") {
		subject = "Fwd: " + subject
	}

	header := fmt.Sprintf("\n\n---------- Forwarded message ----------\nFrom: %s\nDate: %s\nSubject: %s\nTo: %s\n\n",
		pm.From, pm.Date, pm.Subject, pm.To)

	m := &OutgoingMessage{
		From:     from,
		To:       to,
		Subject:  subject,
		TextBody: additionalBody + header + pm.Text(),
	}
	for _, att := range pm.Attachments {
		if data, ok := pm.attachmentData[att.Filename]; ok {
			m.Attachments = append(m.Attachments, Attachment{
				Filename: att.Filename, ContentType: att.ContentType, Data: data,
			})
		}
	}
	return c.Send(ctx, m)
}

// ---------------------------------------------------------------------------
// Organizing
// ---------------------------------------------------------------------------

// SetFlags changes the read and starred state of a message. Nil leaves a flag
// untouched.
func (c *GmailClient) SetFlags(ctx context.Context, h Handle, read, starred *bool) error {
	if err := c.requireOrganize(); err != nil {
		return err
	}
	if read == nil && starred == nil {
		return fmt.Errorf("nothing to change: set read and/or starred")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	if _, err := imapClient.Select(h.Mailbox, false); err != nil {
		return fmt.Errorf("failed to select %s: %w", h.Mailbox, err)
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(h.UID)

	apply := func(flag string, on bool) error {
		// AddFlags/RemoveFlags are untyped constants; name the type explicitly.
		var op imap.FlagsOp = imap.RemoveFlags
		if on {
			op = imap.AddFlags
		}
		return imapClient.UidStore(seqset, imap.FormatFlagsOp(op, true), []interface{}{flag}, nil)
	}
	if read != nil {
		if err := apply(imap.SeenFlag, *read); err != nil {
			return fmt.Errorf("failed to change read state: %w", err)
		}
	}
	if starred != nil {
		if err := apply(imap.FlaggedFlag, *starred); err != nil {
			return fmt.Errorf("failed to change starred state: %w", err)
		}
	}
	return nil
}

// Labels adds and removes Gmail labels without moving the message. Removing
// "\\Inbox" archives it; adding a label files it while leaving it in place.
func (c *GmailClient) Labels(ctx context.Context, h Handle, add, remove []string) error {
	if err := c.requireOrganize(); err != nil {
		return err
	}
	if len(add) == 0 && len(remove) == 0 {
		return fmt.Errorf("nothing to change: supply add and/or remove")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	if _, err := imapClient.Select(h.Mailbox, false); err != nil {
		return fmt.Errorf("failed to select %s: %w", h.Mailbox, err)
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(h.UID)

	if len(add) > 0 {
		if err := storeLabels(imapClient, seqset, "+X-GM-LABELS", add); err != nil {
			return fmt.Errorf("failed to add labels: %w", err)
		}
	}
	if len(remove) > 0 {
		if err := storeLabels(imapClient, seqset, "-X-GM-LABELS", remove); err != nil {
			return fmt.Errorf("failed to remove labels: %w", err)
		}
	}
	return nil
}

// storeLabels issues a UID STORE against Gmail's X-GM-LABELS extension.
func storeLabels(imapClient *client.Client, seqset *imap.SeqSet, item string, labels []string) error {
	args := make([]interface{}, 0, len(labels))
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "\\") {
			// System labels (\Inbox, \Starred, \Important) are atoms, not strings.
			args = append(args, imap.RawString(l))
			continue
		}
		args = append(args, l)
	}
	if len(args) == 0 {
		return nil
	}
	cmd := &imap.Command{
		Name: "UID STORE",
		Arguments: []interface{}{
			imap.RawString(seqset.String()),
			imap.RawString(item),
			args,
		},
	}
	status, err := imapClient.Execute(cmd, nil)
	if err != nil {
		return err
	}
	return status.Err()
}

// Trash moves a message to Trash, from whichever mailbox it is in. Gmail purges
// Trash automatically after 30 days, so this stays recoverable in the meantime —
// there is deliberately no permanent-delete tool.
func (c *GmailClient) Trash(ctx context.Context, h Handle) error {
	if err := c.requireOrganize(); err != nil {
		return err
	}
	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	trash, err := c.trashMailbox(imapClient)
	if err != nil {
		return err
	}
	if h.Mailbox == trash {
		return nil // already there
	}
	if _, err := imapClient.Select(h.Mailbox, false); err != nil {
		return fmt.Errorf("failed to select %s: %w", h.Mailbox, err)
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(h.UID)
	if err := imapClient.UidMove(seqset, trash); err != nil {
		// Servers without MOVE: fall back to copy + expunge.
		if err := imapClient.UidCopy(seqset, trash); err != nil {
			return fmt.Errorf("failed to move message to %s: %w", trash, err)
		}
		return c.expunge(imapClient, h)
	}
	return nil
}

// ListLabels returns the mailboxes/labels the account exposes over IMAP.
func (c *GmailClient) ListLabels(ctx context.Context) ([]string, error) {
	if err := c.requireRead(); err != nil {
		return nil, err
	}
	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	mailboxes := make(chan *imap.MailboxInfo, 100)
	done := make(chan error, 1)
	go func() { done <- imapClient.List("", "*", mailboxes) }()

	var names []string
	for m := range mailboxes {
		names = append(names, m.Name)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to list labels: %w", err)
	}
	return names, nil
}

// ---------------------------------------------------------------------------
// Attachments
// ---------------------------------------------------------------------------

// DownloadedAttachment is the result of saving an attachment to disk.
type DownloadedAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	Path        string `json:"path"`
	Content     string `json:"content,omitempty"` // inlined only for small text files
}

// DownloadAttachment writes an attachment into the workspace and returns its
// relative path. Bytes are deliberately not returned to the caller: a 4MB
// invoice base64-encoded into a tool result would consume the model's context.
func (c *GmailClient) DownloadAttachment(ctx context.Context, h Handle, filename string) (*DownloadedAttachment, error) {
	if err := c.requireRead(); err != nil {
		return nil, err
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}
	raw, err := c.fetchRaw(imapClient, h)
	imapClient.Logout()
	if err != nil {
		return nil, err
	}
	pm, err := ParseMessage(raw, true)
	if err != nil {
		return nil, err
	}

	data, ok := pm.attachmentData[filename]
	if !ok {
		var available []string
		for _, a := range pm.Attachments {
			available = append(available, a.Filename)
		}
		if len(available) == 0 {
			return nil, fmt.Errorf("this message has no attachments")
		}
		return nil, fmt.Errorf("attachment %q not found; this message has: %s", filename, strings.Join(available, ", "))
	}

	var contentType string
	for _, a := range pm.Attachments {
		if a.Filename == filename {
			contentType = a.ContentType
			break
		}
	}

	dest, err := safeAttachmentPath(filename)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return nil, fmt.Errorf("failed to create attachment directory: %w", err)
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write attachment: %w", err)
	}

	out := &DownloadedAttachment{
		Filename:    filepath.Base(dest),
		ContentType: contentType,
		Size:        len(data),
		Path:        dest,
	}
	if len(data) <= maxInlineAttachment && strings.HasPrefix(contentType, "text/") {
		out.Content = string(data)
	}
	return out, nil
}

// safeAttachmentPath resolves a destination under attachmentDir, refusing any
// filename that would escape it. The sandbox oido-core wraps this process in is
// not relied upon for this: the check belongs at the boundary that takes the
// untrusted name, and attachment filenames come from received mail.
func safeAttachmentPath(filename string) (string, error) {
	base := filepath.Base(filepath.Clean("/" + filename))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", fmt.Errorf("invalid attachment filename %q", filename)
	}
	dest := filepath.Join(attachmentDir, base)
	if !withinDir(attachmentDir, dest) {
		return "", fmt.Errorf("invalid attachment filename %q", filename)
	}
	return dest, nil
}

// resolveOutgoingAttachments loads files to attach, refusing anything outside
// the working directory and enforcing the total size cap.
func resolveOutgoingAttachments(paths []string) ([]Attachment, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve working directory: %w", err)
	}

	var out []Attachment
	total := 0
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if filepath.IsAbs(p) {
			return nil, fmt.Errorf("attachment path %q must be relative to the workspace", p)
		}
		if !withinDir(cwd, filepath.Join(cwd, p)) {
			return nil, fmt.Errorf("attachment path %q escapes the workspace", p)
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("attachment %q not found: %w", p, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("attachment %q is a directory", p)
		}
		total += int(info.Size())
		if total > maxAttachmentBytes {
			return nil, fmt.Errorf("attachments exceed the %d MB limit Gmail accepts", maxAttachmentBytes>>20)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("failed to read attachment %q: %w", p, err)
		}
		out = append(out, Attachment{
			Filename:    filepath.Base(p),
			ContentType: contentTypeOf(p),
			Data:        data,
		})
	}
	return out, nil
}

// withinDir reports whether path resolves inside dir, following symlinks where
// they already exist so a link cannot be used to step outside.
func withinDir(dir, path string) bool {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolved
	}
	if resolved, err := filepath.EvalSymlinks(absDir); err == nil {
		absDir = resolved
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func contentTypeOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".csv":
		return "text/csv"
	case ".txt", ".md":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".zip":
		return "application/zip"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		return "application/octet-stream"
	}
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// decodeTransferEncoding wraps a reader to undo a Content-Transfer-Encoding.
func decodeTransferEncoding(encoding string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		data, _ := io.ReadAll(r)
		clean := strings.NewReplacer("\r\n", "", "\n", "", "\r", "", " ", "").Replace(strings.TrimSpace(string(data)))
		decoded, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			// Tolerate truncated or padded-wrong bodies rather than losing everything.
			decoded, _ = base64.RawStdEncoding.DecodeString(strings.TrimRight(clean, "="))
		}
		return bytes.NewReader(decoded)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	default:
		return r
	}
}

// splitAddresses splits an address header into individual addresses, respecting
// quoted display names and angle brackets.
func splitAddresses(header string) []string {
	var out []string
	var cur strings.Builder
	inQuote, inAngle := false, false
	for _, r := range header {
		switch r {
		case '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case '<':
			inAngle = true
			cur.WriteRune(r)
		case '>':
			inAngle = false
			cur.WriteRune(r)
		case ',':
			if inQuote || inAngle {
				cur.WriteRune(r)
				continue
			}
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func containsAddress(list []string, addr string) bool {
	for _, l := range list {
		if a, err := parseAddress(l); err == nil && strings.EqualFold(a, addr) {
			return true
		}
	}
	return false
}

// headerOf pulls a single raw header value out of message bytes. Used for Bcc,
// which mail.ReadMessage exposes but ParsedMessage deliberately does not carry.
func headerOf(raw []byte, key string) string {
	i := bytes.Index(raw, []byte("\r\n\r\n"))
	if i < 0 {
		if i = bytes.Index(raw, []byte("\n\n")); i < 0 {
			i = len(raw)
		}
	}
	lines := strings.Split(strings.ReplaceAll(string(raw[:i]), "\r\n", "\n"), "\n")
	prefix := strings.ToLower(key) + ":"
	for n, line := range lines {
		if !strings.HasPrefix(strings.ToLower(line), prefix) {
			continue
		}
		val := strings.TrimSpace(line[len(prefix):])
		// Continuation lines are indented.
		for _, cont := range lines[n+1:] {
			if cont == "" || (cont[0] != ' ' && cont[0] != '\t') {
				break
			}
			val += " " + strings.TrimSpace(cont)
		}
		return val
	}
	return ""
}

// stripHeader removes a header (and its continuation lines) from raw bytes.
func stripHeader(raw []byte, key string) []byte {
	i := bytes.Index(raw, []byte("\r\n\r\n"))
	sep := "\r\n"
	if i < 0 {
		if i = bytes.Index(raw, []byte("\n\n")); i < 0 {
			return raw
		}
		sep = "\n"
	}
	head := string(raw[:i])
	body := raw[i:]

	lines := strings.Split(head, sep)
	prefix := strings.ToLower(key) + ":"
	var kept []string
	skipping := false
	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			skipping = true
			continue
		}
		if skipping {
			if line != "" && (line[0] == ' ' || line[0] == '\t') {
				continue
			}
			skipping = false
		}
		kept = append(kept, line)
	}
	return append([]byte(strings.Join(kept, sep)), body...)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

func derefList(p *[]string, fallback []string) []string {
	if p == nil {
		return fallback
	}
	return *p
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
