# Oido Gmail MCP Extension

Send, receive, search, and list emails via IMAP/SMTP using the Model Context Protocol.

## Features

- **List Emails**: View recent inbox messages
- **Read Emails**: Fetch full email content by UID
- **Send Emails**: Compose and send messages via SMTP
- **Search Emails**: Filter inbox by subject keyword

## Requirements

- Go 1.26+
- Gmail account with App Password enabled

## Setup

### 1. Generate Gmail App Password

1. Go to your Google Account → Security
2. Enable 2-Step Verification if not already enabled
3. Go to App Passwords
4. Generate a password for "Mail" → "Other (Custom name)" → enter "Oido Studio"
5. Copy the 16-character password

### 2. Configure Extension

Set the following environment variables (or configure via plugin settings):

| Variable | Description | Default |
|----------|-------------|---------|
| `GMAIL_EMAIL` | Your Gmail address | *(required)* |
| `GMAIL_PASSWORD` | Gmail App Password | *(required)* |
| `GMAIL_IMAP_HOST` | IMAP server host | `imap.gmail.com` |
| `GMAIL_IMAP_PORT` | IMAP server port | `993` |
| `GMAIL_SMTP_HOST` | SMTP server host | `smtp.gmail.com` |
| `GMAIL_SMTP_PORT` | SMTP server port | `587` |
| `GMAIL_ALLOW_SEND` | Enable sending emails | `false` |
| `GMAIL_ALLOW_RECEIVE` | Enable reading emails | `true` |

## Build

```bash
make build
```

## Package for Distribution

```bash
make dist
```

This creates `dist/oido-gmail.zip` for upload via the Plugins UI.

## Tools

### `list_emails`
List recent emails from INBOX.

### `read_email`
Read full email content by UID.

### `send_email`
Send an email (requires `GMAIL_ALLOW_SEND=true`).

### `search_emails`
Search emails by subject.

## Architecture

```
┌─────────────┐     stdio      ┌──────────────────┐
│  Qwen CLI   │ ◄────────────► │  oido-gmail-mcp   │
│             │                │                  │
│             │                │  ┌────────────┐  │
│             │                │  │ IMAP Client │  │──► Gmail IMAP
│             │                │  └────────────┘  │
│             │                │  ┌────────────┐  │
│             │                │  │ SMTP Client │  │──► Gmail SMTP
│             │                │  └────────────┘  │
└─────────────┘                └──────────────────┘
```

## License

MIT
