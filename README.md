# Oido Gmail MCP Extension

Send, receive, search, and list emails via IMAP/SMTP using the Model Context Protocol.

## Features

- **List Emails**: View recent inbox messages
- **Read Emails**: Fetch full email content by UID
- **Send Emails**: Compose and send messages via SMTP
- **Search Emails**: Filter inbox by subject keyword
- **Save Drafts**: Compose and save draft emails without sending

## Installation

### Option 1: Upload via Plugins UI (Recommended)

1. Download the latest release zip for your platform from [GitHub Releases](../../releases)
   - Linux: `oido-gmail-linux-amd64.zip`
   - macOS (Apple Silicon): `oido-gmail-darwin-arm64.zip`
2. Open Qwen CLI → Plugins UI
3. Upload the zip file
4. Configure settings (email, password, permissions) in the plugin settings panel

### Option 2: Build from Source

```bash
git clone <repo-url>
cd oido-gmail
make build
```

Then point your plugin configuration to the built `oido-gmail-mcp` binary.

### Option 3: Manual Install from Release Artifacts

```bash
# Download and extract
curl -LO https://github.com/<owner>/<repo>/releases/latest/download/oido-gmail-linux-amd64.zip
unzip oido-gmail-linux-amd64.zip -d oido-gmail

# Run the MCP server
./oido-gmail/oido-gmail-mcp
```

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

### `save_draft`
Save an email as a draft (requires `GMAIL_ALLOW_SEND=true`).

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
