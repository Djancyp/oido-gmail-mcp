---
name: oido-gmail
description: Send, receive, search, and list emails via IMAP/SMTP
---

# Oido Gmail Extension

## Overview

The Oido Gmail extension provides tools to send, receive, search, and list emails via IMAP and SMTP. Use these tools when users ask about email content, want to compose messages, or need to find specific emails.

## Available Tools

### `list_emails`

List recent emails from the INBOX.

**Parameters:**
- `count` (number, optional): Number of recent emails to retrieve (default: 20)

**When to use:**
- User asks "what emails do I have?"
- User wants to see recent inbox
- User wants to check for new messages

**Example Usage:**
```
User: "Show me my last 10 emails"
→ Call list_emails with count: 10
```

**Response Format:**
Returns formatted table with UID, From, Date, Subject.

### `read_email`

Read the full content of a specific email by UID.

**Parameters:**
- `uid` (number, required): Unique ID of the email to read

**When to use:**
- User asks to read a specific email
- User wants to see the full body of a message
- User references an email by UID from a previous list_emails result

**Example Usage:**
```
User: "Read email 12345"
→ Call read_email with uid: 12345
```

**Response Format:**
Returns Subject, From, To, Date, and full body text.

### `send_email`

Send an email via SMTP.

**Parameters:**
- `to` (string, required): Recipient email address
- `subject` (string, required): Email subject line
- `body` (string, required): Email body text

**When to use:**
- User asks to send an email
- User wants to reply to a message
- User wants to compose a new message

**Example Usage:**
```
User: "Send an email to alice@example.com with subject 'Meeting Tomorrow' and body 'Hi, can we meet at 3pm?'"
→ Call send_email with to: "alice@example.com", subject: "Meeting Tomorrow", body: "Hi, can we meet at 3pm?"
```

**Response Format:**
Returns success confirmation with recipient address.

### `search_emails`

Search emails by subject in the INBOX.

**Parameters:**
- `query` (string, required): Search term to match in email subject
- `count` (number, optional): Maximum number of results to return (default: 20)

**When to use:**
- User asks to search for emails
- User wants to find emails about a specific topic
- User wants to filter inbox by keyword

**Example Usage:**
```
User: "Find emails about 'invoice'"
→ Call search_emails with query: "invoice"
```

**Response Format:**
Returns formatted table with UID, From, Date, Subject for matching emails.

## Best Practices

1. **List before reading**: Use list_emails first to show available emails, then read_email for details
2. **UID-based access**: Always use the UID from list_emails/search_emails when calling read_email
3. **Permission-aware**: Sending requires GMAIL_ALLOW_SEND=true, reading requires GMAIL_ALLOW_RECEIVE=true
4. **Default limits**: Respect count limits (default 20 for list/search)
5. **Plain text only**: Body content is sent as plain text (no HTML support currently)
6. **Context**: Mention results are from the user's Gmail inbox

## Example Interactions

### Listing Emails

```
User: "What's in my inbox?"

Assistant: Let me check your recent emails.
[Calls list_emails with count: 20]

Recent Emails (5):

UID    | From                        | Date                        | Subject
-------+-----------------------------+-----------------------------+---------------------------
1001   | boss@company.com            | Mon, 13 Apr 2026 09:00:00   | Q2 Planning Meeting
1002   | alice@example.com           | Sun, 12 Apr 2026 15:30:00   | Re: Project Update
...

Would you like me to read any of these?
```

### Reading an Email

```
User: "Read email 1001"

Assistant: Let me fetch that email for you.
[Calls read_email with uid: 1001]

Subject: Q2 Planning Meeting
From:    boss@company.com
To:      you@gmail.com
Date:    Mon, 13 Apr 2026 09:00:00 +0000
-------
Hi,

I'd like to schedule a Q2 planning meeting for next week.
Please review the attached agenda and let me know your availability.

Best regards
```

### Searching Emails

```
User: "Find emails about 'project'"

Assistant: Let me search your inbox for "project".
[Calls search_emails with query: "project"]

Search Results for "project" (3):

UID    | From                        | Date                        | Subject
-------+-----------------------------+-----------------------------+---------------------------
1002   | alice@example.com           | Sun, 12 Apr 2026 15:30:00   | Re: Project Update
998    | pm@company.com              | Fri, 10 Apr 2026 10:00:00   | Project Timeline Review
...
```

### Sending an Email

```
User: "Send an email to alice@example.com, subject 'Thanks', body 'Thanks for the update. Looking forward to next week.'"

Assistant: I'll send that email for you.
[Calls send_email with to: "alice@example.com", subject: "Thanks", body: "Thanks for the update. Looking forward to next week."]

Email sent successfully to alice@example.com
```

## Limitations

- **IMAP only**: Connects to Gmail via IMAP (not Gmail API)
- **SMTP only**: Sends via SMTP with STARTTLS or SMTPS
- **Plain text bodies**: Only plain text email bodies are read (HTML stripped)
- **INBOX only**: Only reads from INBOX mailbox (no folder navigation)
- **Subject search only**: Search matches only the subject field (not body)
- Requires environment variables: GMAIL_EMAIL, GMAIL_PASSWORD
- **Gmail App Password**: Use a Gmail App Password, not your regular account password
- **Permission controls**: GMAIL_ALLOW_SEND (default: false), GMAIL_ALLOW_RECEIVE (default: true)
- **Connection**: IMAP on port 993 (TLS), SMTP on port 587 (STARTTLS) or 465 (SMTPS)

## Related Commands

- `/list-emails` - List recent emails (custom command)
- `/read-email` - Read full email content (custom command)
- `/send-email` - Send an email (custom command)
- `/search-emails` - Search emails by subject (custom command)

## Triggers

Use these tools when you see:
- "email" or "inbox" or "mailbox"
- "read message" or "show emails"
- "send email" or "compose" or "reply"
- "search" or "find email" or "look for"
- Gmail, IMAP, SMTP, email questions
