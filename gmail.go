package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// EmailSettings holds configuration for email access.
type EmailSettings struct {
	Email        string
	Password     string
	AccessToken  string // OAuth2 access token (XOAUTH2); preferred over Password
	IMAPHost     string
	IMAPPort     int
	SMTPHost     string
	SMTPPort     int
	AllowSend    bool
	AllowReceive bool
}

// DefaultEmailSettings returns settings with only receive enabled by default.
func DefaultEmailSettings() *EmailSettings {
	return &EmailSettings{
		AllowSend:    false,
		AllowReceive: true,
	}
}

// loadDotEnv reads .env file from the binary's directory and sets env vars if not already set.
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
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
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

// parseEmailSettings reads environment variables to configure email access.
func parseEmailSettings() (*EmailSettings, error) {
	loadDotEnv()
	settings := DefaultEmailSettings()

	settings.Email = os.Getenv("GMAIL_EMAIL")
	settings.Password = os.Getenv("GMAIL_PASSWORD")
	settings.AccessToken = os.Getenv("GOOGLE_ACCESS_TOKEN")
	// When connected via OAuth the user may not set GMAIL_EMAIL. Fall back to the
	// email captured during the grant, then to a live userinfo lookup with the
	// token — so an OAuth connection alone fully configures the plugin.
	if settings.Email == "" {
		settings.Email = os.Getenv("GOOGLE_OAUTH_EMAIL")
	}
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

	imapPortStr := os.Getenv("GMAIL_IMAP_PORT")
	if imapPortStr == "" {
		settings.IMAPPort = 993
	} else {
		port, err := strconv.Atoi(imapPortStr)
		if err != nil {
			return nil, fmt.Errorf("invalid GMAIL_IMAP_PORT: %w", err)
		}
		settings.IMAPPort = port
	}

	settings.SMTPHost = os.Getenv("GMAIL_SMTP_HOST")
	if settings.SMTPHost == "" {
		settings.SMTPHost = "smtp.gmail.com"
	}

	smtpPortStr := os.Getenv("GMAIL_SMTP_PORT")
	if smtpPortStr == "" {
		settings.SMTPPort = 587
	} else {
		port, err := strconv.Atoi(smtpPortStr)
		if err != nil {
			return nil, fmt.Errorf("invalid GMAIL_SMTP_PORT: %w", err)
		}
		settings.SMTPPort = port
	}

	if val := os.Getenv("GMAIL_ALLOW_SEND"); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			settings.AllowSend = b
		}
	}
	if val := os.Getenv("GMAIL_ALLOW_RECEIVE"); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			settings.AllowReceive = b
		}
	}

	if settings.Email == "" || (settings.Password == "" && settings.AccessToken == "") {
		log.Println("Warning: Gmail not configured. Set GMAIL_EMAIL + GMAIL_PASSWORD, or connect via Google OAuth. Tools will return errors until configured.")
	}

	return settings, nil
}

// EmailSummary represents a lightweight email listing.
type EmailSummary struct {
	UID     uint32
	Subject string
	From    string
	Date    string
	Seen    bool
}

// EmailDetail represents a full email message.
type EmailDetail struct {
	Subject string
	From    string
	To      string
	Date    string
	Body    string
}

// GmailClient manages IMAP and SMTP connections.
type GmailClient struct {
	settings *EmailSettings
}

// NewGmailClient creates a new Gmail client from environment variables.
func NewGmailClient() (*GmailClient, error) {
	settings, err := parseEmailSettings()
	if err != nil {
		return nil, err
	}

	return &GmailClient{settings: settings}, nil
}

// TestConnection tests the IMAP connection and password, logging the result.
func (c *GmailClient) TestConnection() {
	if c.settings.Email == "" || c.settings.Password == "" {
		log.Printf("IMAP connection SKIPPED - Gmail not configured")
		return
	}
	log.Printf("Testing IMAP connection to %s:%d...", c.settings.IMAPHost, c.settings.IMAPPort)
	client, err := c.getIMAPClient()
	if err != nil {
		log.Printf("IMAP connection FAILED: %v", err)
		log.Printf("Check your GMAIL_EMAIL, GMAIL_PASSWORD, and ensure IMAP is enabled in Gmail settings.")
		return
	}
	client.Logout()
	log.Printf("IMAP connection OK - authentication successful")
}

// getIMAPClient connects to the IMAP server and returns an authenticated client.
func (c *GmailClient) getIMAPClient() (*client.Client, error) {
	if c.settings.Email == "" || (c.settings.Password == "" && c.settings.AccessToken == "") {
		return nil, fmt.Errorf("Gmail not configured: set GMAIL_EMAIL and GMAIL_PASSWORD, or connect via Google OAuth")
	}

	addr := fmt.Sprintf("%s:%d", c.settings.IMAPHost, c.settings.IMAPPort)

	imapClient, err := client.DialTLS(addr, &tls.Config{
		ServerName: c.settings.IMAPHost,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
	}

	// Prefer OAuth2 (XOAUTH2) when an access token is present; else app password.
	if c.settings.AccessToken != "" {
		if err := imapClient.Authenticate(&imapXOAuth2{email: c.settings.Email, token: c.settings.AccessToken}); err != nil {
			imapClient.Logout()
			return nil, fmt.Errorf("IMAP XOAUTH2 auth failed: %w", err)
		}
	} else if err := imapClient.Login(c.settings.Email, c.settings.Password); err != nil {
		imapClient.Logout()
		return nil, fmt.Errorf("IMAP login failed: %w", err)
	}

	return imapClient, nil
}

// ListEmails retrieves a list of recent emails from the INBOX.
func (c *GmailClient) ListEmails(ctx context.Context, count int) ([]EmailSummary, error) {
	if !c.settings.AllowReceive {
		return nil, fmt.Errorf("blocked: receiving emails is not allowed (enable with GMAIL_ALLOW_RECEIVE=true)")
	}

	if count <= 0 {
		count = 20
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	mbox, err := imapClient.Select("INBOX", true)
	if err != nil {
		return nil, fmt.Errorf("failed to select INBOX: %w", err)
	}

	if mbox.Messages == 0 {
		return []EmailSummary{}, nil
	}

	// Calculate sequence range for most recent messages
	from := uint32(1)
	if mbox.Messages >= uint32(count) {
		from = mbox.Messages - uint32(count) + 1
	}
	seqset := new(imap.SeqSet)
	seqset.AddRange(from, mbox.Messages)

	// Fetch UID, FLAGS, and full header
	headerSection := &imap.BodySectionName{
		Peek: true,
		BodyPartName: imap.BodyPartName{
			Specifier: imap.HeaderSpecifier,
		},
	}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, headerSection.FetchItem()}

	messages := make(chan *imap.Message, count)
	go func() {
		_ = imapClient.Fetch(seqset, items, messages)
	}()

	var summaries []EmailSummary
	for msg := range messages {
		if len(summaries) >= count {
			break
		}

		summary := EmailSummary{
			UID: msg.Uid,
		}

		// Check if email is seen
		for _, flag := range msg.Flags {
			if flag == imap.SeenFlag {
				summary.Seen = true
				break
			}
		}

		r := msg.GetBody(headerSection)
		if r != nil {
			headerData, _ := io.ReadAll(r)
			msgHeader, _ := readHeaderFromData(headerData)
			summary.Subject = msgHeader.Get("Subject")
			summary.From = msgHeader.Get("From")
			summary.Date = msgHeader.Get("Date")
		}

		if summary.Subject == "" {
			summary.Subject = "(no subject)"
		}

		summaries = append(summaries, summary)
	}

	return summaries, nil
}

// ReadEmail fetches the full content of a specific email by UID.
func (c *GmailClient) ReadEmail(ctx context.Context, uid uint32) (*EmailDetail, error) {
	if !c.settings.AllowReceive {
		return nil, fmt.Errorf("blocked: receiving emails is not allowed (enable with GMAIL_ALLOW_RECEIVE=true)")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	_, err = imapClient.Select("INBOX", true)
	if err != nil {
		return nil, fmt.Errorf("failed to select INBOX: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	// Fetch full message
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{section.FetchItem()}

	messages := make(chan *imap.Message, 1)
	go func() {
		_ = imapClient.UidFetch(seqset, items, messages)
	}()

	msg := <-messages
	if msg == nil {
		return nil, fmt.Errorf("email with UID %d not found", uid)
	}

	r := msg.GetBody(section)
	if r == nil {
		return nil, fmt.Errorf("failed to get email body")
	}

	rawData, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read email data: %w", err)
	}

	// Parse header
	msgHeader, err := readHeaderFromData(rawData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse email header: %w", err)
	}

	detail := &EmailDetail{}
	detail.Subject = msgHeader.Get("Subject")
	detail.From = msgHeader.Get("From")
	detail.To = msgHeader.Get("To")
	detail.Date = msgHeader.Get("Date")

	if detail.Subject == "" {
		detail.Subject = "(no subject)"
	}

	// Parse body from MIME message
	detail.Body = extractBody(rawData)
	if detail.Body == "" {
		detail.Body = "(no text body found)"
	}

	return detail, nil
}

// SearchEmails searches emails by subject in the INBOX.
func (c *GmailClient) SearchEmails(ctx context.Context, query string, count int) ([]EmailSummary, error) {
	if !c.settings.AllowReceive {
		return nil, fmt.Errorf("blocked: receiving emails is not allowed (enable with GMAIL_ALLOW_RECEIVE=true)")
	}

	if count <= 0 {
		count = 20
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	mbox, err := imapClient.Select("INBOX", true)
	if err != nil {
		return nil, fmt.Errorf("failed to select INBOX: %w", err)
	}

	if mbox.Messages == 0 {
		return []EmailSummary{}, nil
	}

	// Build search criteria
	criteria := imap.NewSearchCriteria()
	criteria.Header.Add("Subject", query)

	seqnums, err := imapClient.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	if len(seqnums) == 0 {
		return []EmailSummary{}, nil
	}

	// Limit results
	if len(seqnums) > count {
		seqnums = seqnums[len(seqnums)-count:]
	}

	seqset := new(imap.SeqSet)
	for _, num := range seqnums {
		seqset.AddNum(num)
	}

	headerSection := &imap.BodySectionName{
		Peek: true,
		BodyPartName: imap.BodyPartName{
			Specifier: imap.HeaderSpecifier,
		},
	}
	items := []imap.FetchItem{imap.FetchUid, headerSection.FetchItem()}

	messages := make(chan *imap.Message, len(seqnums))
	go func() {
		_ = imapClient.Fetch(seqset, items, messages)
	}()

	var summaries []EmailSummary
	for msg := range messages {
		if len(summaries) >= count {
			break
		}

		summary := EmailSummary{UID: msg.Uid}

		r := msg.GetBody(headerSection)
		if r != nil {
			headerData, _ := io.ReadAll(r)
			msgHeader, _ := readHeaderFromData(headerData)
			summary.Subject = msgHeader.Get("Subject")
			summary.From = msgHeader.Get("From")
			summary.Date = msgHeader.Get("Date")
		}

		if summary.Subject == "" {
			summary.Subject = "(no subject)"
		}

		summaries = append(summaries, summary)
	}

	return summaries, nil
}

// SaveDraft saves an email as a draft in [Gmail]/Drafts via IMAP APPEND.
// from is the sender address; defaults to settings.Email if empty (supports Gmail "Send mail as").
func (c *GmailClient) SaveDraft(ctx context.Context, from, to, subject, body string) error {
	if !c.settings.AllowSend {
		return fmt.Errorf("blocked: saving drafts is not allowed (enable with GMAIL_ALLOW_SEND=true)")
	}

	if to == "" || subject == "" {
		return fmt.Errorf("to and subject are required")
	}

	if from == "" {
		from = c.settings.Email
	}

	var msg bytes.Buffer
	msg.WriteString(fmt.Sprintf("From: %s\r\n", from))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	draftsFolder := "[Gmail]/Drafts"
	reader := bytes.NewReader(msg.Bytes())
	if err := imapClient.Append(draftsFolder, nil, time.Now(), reader); err != nil {
		return fmt.Errorf("failed to save draft to %s: %w", draftsFolder, err)
	}

	return nil
}

// SendEmail sends an email via SMTP.
// from is the sender address; defaults to settings.Email if empty (supports Gmail "Send mail as").
func (c *GmailClient) SendEmail(ctx context.Context, from, to, subject, body string) error {
	if !c.settings.AllowSend {
		return fmt.Errorf("blocked: sending emails is not allowed (enable with GMAIL_ALLOW_SEND=true)")
	}

	if to == "" || subject == "" {
		return fmt.Errorf("to and subject are required")
	}

	if from == "" {
		from = c.settings.Email
	}

	var msg bytes.Buffer
	msg.WriteString(fmt.Sprintf("From: %s\r\n", from))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	return c.sendMsgViaSMTP(from, to, msg.Bytes())
}

// sendSMTPS sends an email using implicit TLS (SMTPS on port 465).
func (c *GmailClient) sendSMTPS(addr string, auth smtp.Auth, from, to string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		ServerName: c.settings.SMTPHost,
	})
	if err != nil {
		return fmt.Errorf("SMTPS connection failed: %w", err)
	}
	defer conn.Close()

	smtpClient, err := smtp.NewClient(conn, c.settings.SMTPHost)
	if err != nil {
		return fmt.Errorf("SMTP client creation failed: %w", err)
	}
	defer smtpClient.Close()

	if err := smtpClient.Auth(auth); err != nil {
		return fmt.Errorf("SMTP auth failed: %w", err)
	}

	if err := smtpClient.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM failed: %w", err)
	}

	if err := smtpClient.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO failed: %w", err)
	}

	wr, err := smtpClient.Data()
	if err != nil {
		return fmt.Errorf("DATA command failed: %w", err)
	}

	if _, err := wr.Write(msg); err != nil {
		return fmt.Errorf("failed to write email: %w", err)
	}

	if err := wr.Close(); err != nil {
		return fmt.Errorf("failed to close data: %w", err)
	}

	return smtpClient.Quit()
}

// extractBody extracts the text/plain body from a raw MIME message.
func extractBody(rawData []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(rawData))
	if err != nil {
		return ""
	}

	body, _ := io.ReadAll(msg.Body)
	return strings.TrimSpace(string(body))
}

// readHeaderFromData parses email headers from raw data.
func readHeaderFromData(data []byte) (mail.Header, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return msg.Header, nil
}

// AttachmentInfo describes email attachment metadata.
type AttachmentInfo struct {
	Filename    string
	ContentType string
	Size        int
}

// AttachmentData holds a downloaded attachment.
type AttachmentData struct {
	Filename    string
	ContentType string
	Data        []byte
}

type mimeAttachment struct {
	filename    string
	contentType string
	data        []byte
}

// listEmailsFromFolder retrieves recent email summaries from any IMAP folder.
func (c *GmailClient) listEmailsFromFolder(ctx context.Context, folder string, count int) ([]EmailSummary, error) {
	if !c.settings.AllowReceive {
		return nil, fmt.Errorf("blocked: receiving emails is not allowed (enable with GMAIL_ALLOW_RECEIVE=true)")
	}
	if count <= 0 {
		count = 20
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	mbox, err := imapClient.Select(folder, true)
	if err != nil {
		return nil, fmt.Errorf("failed to select %s: %w", folder, err)
	}
	if mbox.Messages == 0 {
		return []EmailSummary{}, nil
	}

	from := uint32(1)
	if mbox.Messages >= uint32(count) {
		from = mbox.Messages - uint32(count) + 1
	}
	seqset := new(imap.SeqSet)
	seqset.AddRange(from, mbox.Messages)

	headerSection := &imap.BodySectionName{
		Peek: true,
		BodyPartName: imap.BodyPartName{
			Specifier: imap.HeaderSpecifier,
		},
	}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, headerSection.FetchItem()}

	messages := make(chan *imap.Message, count)
	go func() {
		_ = imapClient.Fetch(seqset, items, messages)
	}()

	var summaries []EmailSummary
	for msg := range messages {
		if len(summaries) >= count {
			break
		}
		summary := EmailSummary{UID: msg.Uid}
		for _, flag := range msg.Flags {
			if flag == imap.SeenFlag {
				summary.Seen = true
				break
			}
		}
		r := msg.GetBody(headerSection)
		if r != nil {
			headerData, _ := io.ReadAll(r)
			msgHeader, _ := readHeaderFromData(headerData)
			summary.Subject = msgHeader.Get("Subject")
			summary.From = msgHeader.Get("From")
			summary.Date = msgHeader.Get("Date")
		}
		if summary.Subject == "" {
			summary.Subject = "(no subject)"
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// fetchRawEmail fetches the raw bytes of an email by UID from the given folder.
func (c *GmailClient) fetchRawEmail(imapClient *client.Client, uid uint32, folder string) ([]byte, error) {
	if folder == "" {
		folder = "INBOX"
	}
	if _, err := imapClient.Select(folder, true); err != nil {
		return nil, fmt.Errorf("failed to select %s: %w", folder, err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{section.FetchItem()}

	messages := make(chan *imap.Message, 1)
	go func() {
		_ = imapClient.UidFetch(seqset, items, messages)
	}()

	msg := <-messages
	if msg == nil {
		return nil, fmt.Errorf("email with UID %d not found in %s", uid, folder)
	}

	r := msg.GetBody(section)
	if r == nil {
		return nil, fmt.Errorf("failed to get body for UID %d", uid)
	}

	return io.ReadAll(r)
}

// sendMsgViaSMTP sends pre-built email bytes to a recipient via SMTP.
// from is the envelope sender; defaults to settings.Email if empty (supports Gmail "Send mail as").
func (c *GmailClient) sendMsgViaSMTP(from, to string, msg []byte) error {
	if from == "" {
		from = c.settings.Email
	}
	addr := fmt.Sprintf("%s:%d", c.settings.SMTPHost, c.settings.SMTPPort)
	var auth smtp.Auth
	if c.settings.AccessToken != "" {
		auth = &smtpXOAuth2{email: c.settings.Email, token: c.settings.AccessToken}
	} else {
		auth = smtp.PlainAuth("", c.settings.Email, c.settings.Password, c.settings.SMTPHost)
	}

	if c.settings.SMTPPort == 465 {
		return c.sendSMTPS(addr, auth, from, to, msg)
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
	if err := smtpClient.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM failed: %w", err)
	}
	if err := smtpClient.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO failed: %w", err)
	}
	wr, err := smtpClient.Data()
	if err != nil {
		return fmt.Errorf("DATA command failed: %w", err)
	}
	if _, err := wr.Write(msg); err != nil {
		return fmt.Errorf("failed to write email data: %w", err)
	}
	if err := wr.Close(); err != nil {
		return fmt.Errorf("failed to close data writer: %w", err)
	}
	return smtpClient.Quit()
}

// decodeTransferEncoding wraps r to decode the given Content-Transfer-Encoding.
func decodeTransferEncoding(encoding string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		data, _ := io.ReadAll(r)
		clean := strings.NewReplacer("\r\n", "", "\n", "", "\r", "").Replace(strings.TrimSpace(string(data)))
		decoded, _ := base64.StdEncoding.DecodeString(clean)
		return bytes.NewReader(decoded)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	default:
		return r
	}
}

// extractMIMEAttachments parses raw email bytes and returns all attachments.
func extractMIMEAttachments(rawData []byte) []mimeAttachment {
	msg, err := mail.ReadMessage(bytes.NewReader(rawData))
	if err != nil {
		return nil
	}
	return parseMIMEBody(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, 0)
}

// parseMIMEBody recursively extracts attachments from a MIME body.
func parseMIMEBody(contentType, transferEncoding string, body io.Reader, depth int) []mimeAttachment {
	if depth > 4 {
		return nil
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return nil
	}

	boundary := params["boundary"]
	if boundary == "" {
		return nil
	}

	decoded := decodeTransferEncoding(transferEncoding, body)
	mr := multipart.NewReader(decoded, boundary)

	var attachments []mimeAttachment
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}

		partCT := part.Header.Get("Content-Type")
		partTE := part.Header.Get("Content-Transfer-Encoding")
		partDisp := part.Header.Get("Content-Disposition")

		var filename string
		if partDisp != "" {
			_, dispParams, _ := mime.ParseMediaType(partDisp)
			filename = dispParams["filename"]
		}
		if filename == "" && partCT != "" {
			_, ctParams, _ := mime.ParseMediaType(partCT)
			filename = ctParams["name"]
		}

		if filename != "" {
			data, _ := io.ReadAll(decodeTransferEncoding(partTE, part))
			attachments = append(attachments, mimeAttachment{
				filename:    filename,
				contentType: partCT,
				data:        data,
			})
			continue
		}

		attachments = append(attachments, parseMIMEBody(partCT, partTE, part, depth+1)...)
	}

	return attachments
}

// DeleteEmail moves an email from INBOX to [Gmail]/Trash.
func (c *GmailClient) DeleteEmail(ctx context.Context, uid uint32) error {
	if !c.settings.AllowSend {
		return fmt.Errorf("blocked: deleting emails is not allowed (enable with GMAIL_ALLOW_SEND=true)")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	if _, err := imapClient.Select("INBOX", false); err != nil {
		return fmt.Errorf("failed to select INBOX: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	if err := imapClient.UidCopy(seqset, "[Gmail]/Trash"); err != nil {
		return fmt.Errorf("failed to copy to Trash: %w", err)
	}

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	if err := imapClient.UidStore(seqset, item, []interface{}{imap.DeletedFlag}, nil); err != nil {
		return fmt.Errorf("failed to mark as deleted: %w", err)
	}

	return imapClient.Expunge(nil)
}

// MarkEmail marks an email as read or unread in the given folder (default: INBOX).
func (c *GmailClient) MarkEmail(ctx context.Context, uid uint32, folder string, read bool) error {
	if folder == "" {
		folder = "INBOX"
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	if _, err := imapClient.Select(folder, false); err != nil {
		return fmt.Errorf("failed to select %s: %w", folder, err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	var op imap.FlagsOp
	if read {
		op = imap.AddFlags
	} else {
		op = imap.RemoveFlags
	}

	item := imap.FormatFlagsOp(op, true)
	return imapClient.UidStore(seqset, item, []interface{}{imap.SeenFlag}, nil)
}

// ListLabels returns all available IMAP folders/labels.
func (c *GmailClient) ListLabels(ctx context.Context) ([]string, error) {
	if !c.settings.AllowReceive {
		return nil, fmt.Errorf("blocked: receiving emails is not allowed (enable with GMAIL_ALLOW_RECEIVE=true)")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}
	defer imapClient.Logout()

	mailboxes := make(chan *imap.MailboxInfo, 100)
	go func() {
		_ = imapClient.List("", "*", mailboxes)
	}()

	var labels []string
	for mbox := range mailboxes {
		labels = append(labels, mbox.Name)
	}
	return labels, nil
}

// MoveEmail moves an email from srcFolder to dstFolder.
func (c *GmailClient) MoveEmail(ctx context.Context, uid uint32, srcFolder, dstFolder string) error {
	if !c.settings.AllowSend {
		return fmt.Errorf("blocked: moving emails is not allowed (enable with GMAIL_ALLOW_SEND=true)")
	}
	if srcFolder == "" {
		srcFolder = "INBOX"
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	if _, err := imapClient.Select(srcFolder, false); err != nil {
		return fmt.Errorf("failed to select %s: %w", srcFolder, err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	if err := imapClient.UidCopy(seqset, dstFolder); err != nil {
		return fmt.Errorf("failed to copy to %s: %w", dstFolder, err)
	}

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	if err := imapClient.UidStore(seqset, item, []interface{}{imap.DeletedFlag}, nil); err != nil {
		return fmt.Errorf("failed to mark source as deleted: %w", err)
	}

	return imapClient.Expunge(nil)
}

// ReplyEmail sends a reply to the email with the given UID.
// from is the sender address; defaults to settings.Email if empty (supports Gmail "Send mail as").
func (c *GmailClient) ReplyEmail(ctx context.Context, from string, uid uint32, body string) error {
	if !c.settings.AllowSend {
		return fmt.Errorf("blocked: sending emails is not allowed (enable with GMAIL_ALLOW_SEND=true)")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}

	rawData, err := c.fetchRawEmail(imapClient, uid, "INBOX")
	imapClient.Logout()
	if err != nil {
		return err
	}

	origHeader, err := readHeaderFromData(rawData)
	if err != nil {
		return fmt.Errorf("failed to parse original email: %w", err)
	}

	replyTo := origHeader.Get("Reply-To")
	if replyTo == "" {
		replyTo = origHeader.Get("From")
	}
	addr, err := mail.ParseAddress(replyTo)
	if err != nil {
		return fmt.Errorf("failed to parse reply-to address %q: %w", replyTo, err)
	}

	subject := origHeader.Get("Subject")
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	messageID := origHeader.Get("Message-Id")
	references := origHeader.Get("References")
	if references != "" && messageID != "" {
		references = references + " " + messageID
	} else if messageID != "" {
		references = messageID
	}

	if from == "" {
		from = c.settings.Email
	}

	var msg bytes.Buffer
	msg.WriteString(fmt.Sprintf("From: %s\r\n", from))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", addr.Address))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	if messageID != "" {
		msg.WriteString(fmt.Sprintf("In-Reply-To: %s\r\n", messageID))
	}
	if references != "" {
		msg.WriteString(fmt.Sprintf("References: %s\r\n", references))
	}
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	return c.sendMsgViaSMTP(from, addr.Address, msg.Bytes())
}

// ForwardEmail forwards an email to the given recipient with optional added text.
// from is the sender address; defaults to settings.Email if empty (supports Gmail "Send mail as").
func (c *GmailClient) ForwardEmail(ctx context.Context, from string, uid uint32, to, additionalBody string) error {
	if !c.settings.AllowSend {
		return fmt.Errorf("blocked: sending emails is not allowed (enable with GMAIL_ALLOW_SEND=true)")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}

	rawData, err := c.fetchRawEmail(imapClient, uid, "INBOX")
	imapClient.Logout()
	if err != nil {
		return err
	}

	origHeader, err := readHeaderFromData(rawData)
	if err != nil {
		return fmt.Errorf("failed to parse original email: %w", err)
	}

	subject := origHeader.Get("Subject")
	lower := strings.ToLower(subject)
	if !strings.HasPrefix(lower, "fwd:") && !strings.HasPrefix(lower, "fw:") {
		subject = "Fwd: " + subject
	}

	if from == "" {
		from = c.settings.Email
	}

	origBody := extractBody(rawData)

	var msg bytes.Buffer
	msg.WriteString(fmt.Sprintf("From: %s\r\n", from))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	if additionalBody != "" {
		msg.WriteString(additionalBody)
		msg.WriteString("\r\n\r\n")
	}
	msg.WriteString("---------- Forwarded message ----------\r\n")
	msg.WriteString(fmt.Sprintf("From: %s\r\n", origHeader.Get("From")))
	msg.WriteString(fmt.Sprintf("Date: %s\r\n", origHeader.Get("Date")))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", origHeader.Get("Subject")))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", origHeader.Get("To")))
	msg.WriteString("\r\n")
	msg.WriteString(origBody)

	return c.sendMsgViaSMTP(from, to, msg.Bytes())
}

// ListDrafts returns recent drafts from [Gmail]/Drafts.
func (c *GmailClient) ListDrafts(ctx context.Context, count int) ([]EmailSummary, error) {
	return c.listEmailsFromFolder(ctx, "[Gmail]/Drafts", count)
}

// StarEmail adds or removes the \Flagged (starred) flag on an email in INBOX.
func (c *GmailClient) StarEmail(ctx context.Context, uid uint32, star bool) error {
	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	if _, err := imapClient.Select("INBOX", false); err != nil {
		return fmt.Errorf("failed to select INBOX: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	var op imap.FlagsOp
	if star {
		op = imap.AddFlags
	} else {
		op = imap.RemoveFlags
	}

	item := imap.FormatFlagsOp(op, true)
	return imapClient.UidStore(seqset, item, []interface{}{imap.FlaggedFlag}, nil)
}

// GetAttachments lists all attachments for the given email UID.
func (c *GmailClient) GetAttachments(ctx context.Context, uid uint32) ([]AttachmentInfo, error) {
	if !c.settings.AllowReceive {
		return nil, fmt.Errorf("blocked: receiving emails is not allowed (enable with GMAIL_ALLOW_RECEIVE=true)")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}

	rawData, err := c.fetchRawEmail(imapClient, uid, "INBOX")
	imapClient.Logout()
	if err != nil {
		return nil, err
	}

	parts := extractMIMEAttachments(rawData)
	infos := make([]AttachmentInfo, 0, len(parts))
	for _, p := range parts {
		infos = append(infos, AttachmentInfo{
			Filename:    p.filename,
			ContentType: p.contentType,
			Size:        len(p.data),
		})
	}
	return infos, nil
}

// DownloadAttachment retrieves the raw bytes of a named attachment from an email.
func (c *GmailClient) DownloadAttachment(ctx context.Context, uid uint32, filename string) (*AttachmentData, error) {
	if !c.settings.AllowReceive {
		return nil, fmt.Errorf("blocked: receiving emails is not allowed (enable with GMAIL_ALLOW_RECEIVE=true)")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return nil, err
	}

	rawData, err := c.fetchRawEmail(imapClient, uid, "INBOX")
	imapClient.Logout()
	if err != nil {
		return nil, err
	}

	for _, p := range extractMIMEAttachments(rawData) {
		if strings.EqualFold(p.filename, filename) {
			return &AttachmentData{
				Filename:    p.filename,
				ContentType: p.contentType,
				Data:        p.data,
			}, nil
		}
	}
	return nil, fmt.Errorf("attachment %q not found in email UID %d", filename, uid)
}

// EmptyTrash permanently deletes all emails in [Gmail]/Trash.
func (c *GmailClient) EmptyTrash(ctx context.Context) error {
	if !c.settings.AllowSend {
		return fmt.Errorf("blocked: emptying trash is not allowed (enable with GMAIL_ALLOW_SEND=true)")
	}

	imapClient, err := c.getIMAPClient()
	if err != nil {
		return err
	}
	defer imapClient.Logout()

	mbox, err := imapClient.Select("[Gmail]/Trash", false)
	if err != nil {
		return fmt.Errorf("failed to select Trash: %w", err)
	}
	if mbox.Messages == 0 {
		return nil
	}

	seqset := new(imap.SeqSet)
	seqset.AddRange(1, mbox.Messages)

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	if err := imapClient.Store(seqset, item, []interface{}{imap.DeletedFlag}, nil); err != nil {
		return fmt.Errorf("failed to mark messages for deletion: %w", err)
	}

	return imapClient.Expunge(nil)
}
