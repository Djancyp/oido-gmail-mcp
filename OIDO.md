# Oido Gmail Extension

Send, receive, search, and list emails via IMAP/SMTP. This extension provides MCP tools for email management.

## Available Tools

### `list_emails`
List recent emails from the INBOX.

**Parameters:**
- `count` (number, optional): Number of recent emails to retrieve (default: 20)

**Returns:** Formatted table with UID, From, Date, Subject.

### `read_email`
Read the full content of a specific email by UID.

**Parameters:**
- `uid` (number, required): Unique ID of the email to read

**Returns:** Subject, From, To, Date, and full body text.

### `send_email`
Send an email via SMTP.

**Parameters:**
- `to` (string, required): Recipient email address
- `subject` (string, required): Email subject line
- `body` (string, required): Email body text

**Returns:** Success confirmation with recipient address.

### `search_emails`
Search emails by subject in the INBOX.

**Parameters:**
- `query` (string, required): Search term to match in email subject
- `count` (number, optional): Maximum number of results to return (default: 20)

**Returns:** Formatted table with matching emails (UID, From, Date, Subject).

## Example Usage

```
User: What's in my inbox?

Assistant: I'll check your recent emails.

[Uses list_emails tool]

Recent Emails (5):

UID    | From                        | Date                        | Subject
-------+-----------------------------+-----------------------------+---------------------------
1001   | boss@company.com            | Mon, 13 Apr 2026 09:00:00   | Q2 Planning Meeting
1002   | alice@example.com           | Sun, 12 Apr 2026 15:30:00   | Re: Project Update
...
```

```
User: Send an email to alice@example.com with subject 'Thanks' and body 'Thanks for the update.'

Assistant: I'll send that email for you.

[Uses send_email tool]

Email sent successfully to alice@example.com
```

## When to Use

- User asks to read, list, search, or send emails
- User wants to check inbox
- User wants to compose or reply to a message
- User asks about email content or subject

## Notes

- **IMAP for reading**: Connects via IMAP to fetch emails from INBOX
- **SMTP for sending**: Sends via SMTP with STARTTLS (port 587) or SMTPS (port 465)
- **Configurable permissions**: Send and receive can be individually enabled/disabled
- **Default read-only**: Only receiving enabled by default (GMAIL_ALLOW_RECEIVE=true, GMAIL_ALLOW_SEND=false)
- **Environment variables for connection**:
  - `GMAIL_EMAIL` (required): Email address for authentication
  - `GMAIL_PASSWORD` (required): App password (use Gmail App Password, not account password)
  - `GMAIL_IMAP_HOST` (default: imap.gmail.com): IMAP server host
  - `GMAIL_IMAP_PORT` (default: 993): IMAP server port
  - `GMAIL_SMTP_HOST` (default: smtp.gmail.com): SMTP server host
  - `GMAIL_SMTP_PORT` (default: 587): SMTP server port
- **Row limits**: Default 20 emails for list/search
- **Gmail App Password**: You must generate an App Password from Google Account settings (2-Step Verification → App Passwords). Your regular Gmail password will not work.
