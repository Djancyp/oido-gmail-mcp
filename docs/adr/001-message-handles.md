# ADR-001: Message Handles Instead of Bare UIDs

## Status

Accepted.

## Context

Every tool in `oido-gmail` identified a message by a bare `uint32` UID (`EmailSummary.UID`,
`gmail.go:147`). IMAP UIDs are unique only **within one mailbox**, and the tools did not agree on
which mailbox they were in:

| Tool | Mailbox its UIDs came from |
| --- | --- |
| `gmail_list_emails` | `INBOX` |
| `gmail_search_emails` | All Mail (`\All`, resolved at runtime) |
| `gmail_list_drafts` | `[Gmail]/Drafts` |
| `read`, `star`, `unstar`, `delete`, `reply`, `forward`, `get_attachments`, `download_attachment` | `INBOX`, hardcoded |

So a UID obtained from `list_drafts` or `search_emails` and passed to any reader was interpreted
against `INBOX`. The visible symptom was benign — an agent could not read the recipient of a draft
it had just listed, and had to ask the user who the draft was addressed to. The latent symptom was
not: when a message in `INBOX` happens to hold the same UID number, the reader **silently returns a
different message**, and a mutating tool acts on it. Nothing in the type system or the tool schemas
prevented this; a `uint32` from any source was accepted by every tool.

The plugin is used for order and invoice processing, where the mailbox is treated as a record of
what was agreed. Silently operating on the wrong message is therefore a correctness problem with
business consequences, not just an ergonomic one.

## Decision

Every tool identifies a message by an **opaque Message Handle**: a single string that encodes the
mailbox together with the UID within it (`base64("<mailbox>:<uid>")`, base64'd because Gmail mailbox
names contain `:` and `/`). Tools take `id string`, never `uid uint32`. Each tool decodes the handle
and `SELECT`s the mailbox the handle names. No tool defaults to `INBOX`.

Handles are produced only by tools that list or search, and consumed only by tools that read or
mutate. They are not constructed by the model and their internal structure is not documented to it.

Because a handle is a **positional** reference, it is invalidated when a message moves between
mailboxes — including by `update_draft`, which necessarily replaces the message it edits. A stale
handle **fails loudly** rather than resolving to whatever now occupies that position.

## Alternatives Considered

- **Explicit `folder` parameter on every tool**, defaulting to `INBOX`. Smallest diff and no encoding
  step, but it makes correctness depend on the model remembering to pass the folder on every call.
  Omitting it reproduces exactly the silent wrong-message failure this ADR exists to remove, and does
  so in the failure mode that is hardest to notice. Rejected: a contract that is correct only when
  the caller remembers is not a fix.

- **Gmail message IDs (`X-GM-MSGID`)** as the identifier. Globally unique per account and stable
  across mailbox moves, which would make handles survive the very invalidation described above. But
  every operation would need an `X-GM-MSGID` search to resolve back to a UID before it could act —
  an extra round trip on each call — and the identifier does not exist on non-Gmail IMAP servers,
  which this client explicitly supports via its plain-subject search fallback (`gmail.go:409`).
  Rejected: it buys stability across moves that a single chat session does not need, at the cost of
  per-call latency and the non-Gmail path.

## Consequences

### Positive

- Cross-mailbox misidentification becomes structurally impossible rather than merely documented.
  A handle from `list_drafts` cannot be interpreted against `INBOX`, because it carries its mailbox.
- Drafts, sent mail, archived mail and inbox mail are all readable and mutable through one set of
  tools. The eight `INBOX`-hardcoded operations become mailbox-agnostic with no new tools.
- Removes the reason `gmail_list_drafts` existed as a separate tool, enabling the consolidation of
  the listing tools into `gmail_search`.

### Negative

- Handles go stale when a message moves, and `update_draft` invalidates its own input handle by
  design. Callers must use the handle returned by the update, not the one they passed in. This is a
  real ergonomic cost accepted deliberately: the alternative that avoids it (`X-GM-MSGID`) was
  rejected above.
- Renaming `uid` to `id` is a breaking change to the tool schemas. Anything naming the old parameter
  — saved automations, n8n workflow nodes, user-written prompts — must be updated.
- The encoding is one more thing to get right; a malformed or hand-constructed handle must be
  rejected rather than partially interpreted.
