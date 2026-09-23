# Oido Gmail MCP Extension

Send, receive, search, and list emails via IMAP/SMTP using the Model Context Protocol.

## Features

- **List Emails**: View recent inbox messages
- **Read Emails**: Fetch full email content by UID
- **Send Emails**: Compose and send messages via SMTP
- **Search Emails**: Full Gmail search syntax (is:unread, from:, label:, has:attachment, dates, free text)
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

### Reading

| Tool | Purpose |
|------|---------|
| `gmail_search(query, count)` | Search or list anywhere (inbox, sent, drafts, trash, all mail) using full Gmail syntax. Returns ids used by every other tool. |
| `gmail_read(id, format)` | Read one message in full: headers, body (`text`/`html`/`both`), attachment list. |
| `gmail_list_labels()` | List the labels/mailboxes on the account. |
| `gmail_download_attachment(id, filename)` | Save an attachment into the workspace and return its path. |

### Composing

| Tool | Sends? |
|------|--------|
| `gmail_save_draft(...)` | No — returns a draft id |
| `gmail_update_draft(id, ...)` | No — partial update, returns a **new** draft id |
| `gmail_delete_draft(id)` | No |
| `gmail_draft_reply(id, body, reply_all)` | No |
| `gmail_send(...)` | Yes (requires `GMAIL_ALLOW_SEND=true`) |
| `gmail_send_draft(id)` | Yes |
| `gmail_reply(id, body, reply_all)` | Yes |
| `gmail_forward(id, to, additional_body)` | Yes — carries attachments |

### Organizing

| Tool | Purpose |
|------|---------|
| `gmail_set_flags(id, read, starred)` | Mark read/unread and/or starred/unstarred. |
| `gmail_labels(id, add, remove)` | Add or remove Gmail labels. |
| `gmail_trash(id)` | Move a message to Trash from any mailbox (recoverable for 30 days). |

### TypeSafe judgments (optional — needs `OIDO_TYPESAFE_API_KEY`)

| Tool | Purpose |
|------|---------|
| `gmail_triage(query, count)` | Search + rank by importance and reply-need in one pass. |
| `gmail_classify(ids, categories)` | Sort known ids into caller-given categories. |
| `gmail_suggest_labels(ids, apply)` | Suggest (and optionally apply) an existing label per message. |
| `gmail_phishing_check(ids)` | Flag likely phishing/social-engineering. Advisory only. |
| `gmail_digest(query, count)` | Grouped digest (action_required / fyi / newsletter / notification) with importance and reply-need per message. |

See `OIDO.md` for full parameter details and usage guidance.

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
