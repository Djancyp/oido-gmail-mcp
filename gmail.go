package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
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

	if settings.Email == "" || settings.Password == "" {
		return nil, fmt.Errorf("missing required env vars: GMAIL_EMAIL and GMAIL_PASSWORD must be set")
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

// getIMAPClient connects to the IMAP server and returns an authenticated client.
func (c *GmailClient) getIMAPClient() (*client.Client, error) {
	addr := fmt.Sprintf("%s:%d", c.settings.IMAPHost, c.settings.IMAPPort)

	imapClient, err := client.DialTLS(addr, &tls.Config{
		ServerName: c.settings.IMAPHost,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
	}

	if err := imapClient.Login(c.settings.Email, c.settings.Password); err != nil {
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
func (c *GmailClient) SaveDraft(ctx context.Context, to, subject, body string) error {
	if !c.settings.AllowSend {
		return fmt.Errorf("blocked: saving drafts is not allowed (enable with GMAIL_ALLOW_SEND=true)")
	}

	if to == "" || subject == "" {
		return fmt.Errorf("to and subject are required")
	}

	var msg bytes.Buffer
	fromAddr := c.settings.Email
	msg.WriteString(fmt.Sprintf("From: %s\r\n", fromAddr))
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
func (c *GmailClient) SendEmail(ctx context.Context, to, subject, body string) error {
	if !c.settings.AllowSend {
		return fmt.Errorf("blocked: sending emails is not allowed (enable with GMAIL_ALLOW_SEND=true)")
	}

	if to == "" || subject == "" {
		return fmt.Errorf("to and subject are required")
	}

	// Build email message
	var msg bytes.Buffer

	fromAddr := c.settings.Email
	msg.WriteString(fmt.Sprintf("From: %s\r\n", fromAddr))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	// Send via SMTP
	addr := fmt.Sprintf("%s:%d", c.settings.SMTPHost, c.settings.SMTPPort)

	auth := smtp.PlainAuth("", c.settings.Email, c.settings.Password, c.settings.SMTPHost)

	if c.settings.SMTPPort == 465 {
		// Implicit TLS (SMTPS)
		return c.sendSMTPS(addr, auth, c.settings.Email, to, msg.Bytes())
	}

	// STARTTLS
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

	if err := smtpClient.Mail(c.settings.Email); err != nil {
		return fmt.Errorf("MAIL FROM failed: %w", err)
	}

	if err := smtpClient.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO failed: %w", err)
	}

	wr, err := smtpClient.Data()
	if err != nil {
		return fmt.Errorf("DATA command failed: %w", err)
	}

	if _, err := wr.Write(msg.Bytes()); err != nil {
		return fmt.Errorf("failed to write email data: %w", err)
	}

	if err := wr.Close(); err != nil {
		return fmt.Errorf("failed to close data writer: %w", err)
	}

	return smtpClient.Quit()
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
