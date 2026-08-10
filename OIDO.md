# Oido Gmail Extension

Read, search, organize, draft and send email over IMAP/SMTP.

## Message ids

Every tool that acts on a message takes an `id` from `gmail_search`. **Copy ids
verbatim; never construct or guess one.** An id names both the message and the
mailbox it lives in, so the same tools work on inbox, sent, archived, trashed
and draft messages alike.

Ids are positional. When a message moves — including when you edit a draft — its
old id stops working and the tool tells you so. Re-run `gmail_search` to get a
current one. `gmail_update_draft` returns the new id directly.

## Tools

### Reading

| Tool | Purpose |
| --- | --- |
| `gmail_search(query, count)` | Search or list anywhere. Returns ids, sender, **recipient**, date, flags, attachment presence. |
| `gmail_read(id, format)` | One message in full: headers (To, Cc, Reply-To), body, attachment list. |
| `gmail_list_labels()` | Labels/mailboxes on the account. |
| `gmail_download_attachment(id, filename)` | Saves the file into the workspace, returns its path. |

`gmail_search` takes full Gmail syntax and replaces any separate list tool:

- `in:inbox` (the default), `in:drafts`, `in:sent`, `in:trash`, `in:anywhere`
- `is:unread`, `is:starred`, `has:attachment`
- `from:`, `to:`, `subject:`, `label:`
- `newer_than:7d`, `after:2026/01/01`
- free text, combined freely: `is:unread from:boss@co.com has:attachment`

`gmail_read`'s `format` is `text` (default), `html`, or `both`. **Use `both`
before editing a draft** so its formatting survives the edit.

### Composing

| Tool | Sends? |
| --- | --- |
| `gmail_save_draft(...)` | No — returns a draft id |
| `gmail_update_draft(id, ...)` | No — returns a **new** draft id |
| `gmail_delete_draft(id)` | No |
| `gmail_draft_reply(id, body, reply_all)` | No |
| `gmail_send(...)` | Yes |
| `gmail_send_draft(id)` | Yes |
| `gmail_reply(id, body, reply_all)` | Yes |
| `gmail_forward(id, to, additional_body)` | Yes — carries attachments |

Compose tools take `to`, `cc` and `bcc` as **arrays**, plus `subject`, `body`,
optional `body_html`, and optional `attachments` (workspace-relative paths).

`gmail_update_draft` is a **partial** update: supply only what changes.
Everything you omit — recipient, subject, Cc, Bcc, attachments — is preserved.
So to rewrite a draft's wording you need only its id and the new `body`; you do
**not** need to ask the user who it was addressed to. To clear a field, pass it
explicitly empty.

### Organizing

| Tool | Purpose |
| --- | --- |
| `gmail_set_flags(id, read, starred)` | Both optional; omitted fields unchanged. |
| `gmail_labels(id, add, remove)` | Add or remove labels. |
| `gmail_trash(id)` | Move to Trash from any mailbox. |

Gmail messages hold several labels at once, so **adding a label files a message
without moving it**. Archive by removing `\Inbox`. Move by adding the
destination and removing the source. System labels take a backslash: `\Inbox`,
`\Starred`, `\Important`.

There is no permanent-delete tool. `gmail_trash` is recoverable; Gmail purges
Trash after 30 days.

## Permissions

Three independent settings, drawn around *does this leave the building?*

| Setting | Covers | Default |
| --- | --- | --- |
| `GMAIL_ALLOW_READ` | Reading messages, labels, attachments | on |
| `GMAIL_ALLOW_ORGANIZE` | Flags, labels, trash, and all draft operations | on |
| `GMAIL_ALLOW_SEND` | Transmitting: send, reply, forward, send_draft | **off** |

Drafting never requires send permission. If sending is blocked, save a draft and
tell the user it is waiting for them to review and send.

## Working patterns

**Rewrite a draft.** `gmail_search("in:drafts")` → `gmail_read(id, format:"both")`
→ `gmail_update_draft(id, body:"…")`. Use the returned new id from then on. Do
not save a new draft and delete the old one; that is what `gmail_update_draft`
is for.

**Reply for review.** `gmail_draft_reply(id, body)` — no send permission needed.
Add `reply_all: true` to include everyone on the original.

**Forward an attachment elsewhere.** `gmail_download_attachment` returns a path;
pass that path in `attachments` to `gmail_send`.

## Notes

- **Auth is OAuth.** The user connects their Google account in extension
  settings; no password is configured. If tools report "Gmail not connected",
  the user must reconnect and grant the `https://mail.google.com/` scope.
- **Attachments are written to disk, not returned inline**, so a large invoice
  does not flood the conversation. Small text files are inlined as a convenience.
- **Reading is bounded**: 20 results by default.
