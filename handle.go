package main

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Handle names a single message by the mailbox it lives in plus its UID within
// that mailbox. IMAP UIDs are unique only inside one mailbox, so a bare UID is
// ambiguous across INBOX / Drafts / All Mail — see docs/adr/001-message-handles.md.
type Handle struct {
	Mailbox string
	UID     uint32
}

// String encodes the handle as the opaque id exposed to MCP clients. The
// encoding is deliberately not documented to callers: handles are produced by
// list/search tools and consumed verbatim, never constructed by the model.
func (h Handle) String() string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(h.Mailbox + ":" + strconv.FormatUint(uint64(h.UID), 10)),
	)
}

// ParseHandle decodes an id produced by Handle.String. It rejects anything it
// did not produce rather than guessing a mailbox, so a hand-built or stale-format
// id fails loudly instead of silently resolving against INBOX.
func ParseHandle(s string) (Handle, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Handle{}, fmt.Errorf("id is required (use the id returned by gmail_search)")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Handle{}, fmt.Errorf("invalid id %q: ids must be copied verbatim from gmail_search or gmail_list_drafts results, not constructed", s)
	}
	// Mailbox names may contain '/', and in principle ':', so split on the last colon.
	decoded := string(raw)
	i := strings.LastIndex(decoded, ":")
	if i <= 0 || i == len(decoded)-1 {
		return Handle{}, fmt.Errorf("invalid id %q: malformed", s)
	}
	uid, err := strconv.ParseUint(decoded[i+1:], 10, 32)
	if err != nil || uid == 0 {
		return Handle{}, fmt.Errorf("invalid id %q: malformed message number", s)
	}
	return Handle{Mailbox: decoded[:i], UID: uint32(uid)}, nil
}
