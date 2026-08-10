package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewMCPHandler creates a new MCP handler for Gmail tools.
func NewMCPHandler(gc *GmailClient) *MCPHandler {
	return &MCPHandler{gc: gc}
}

// MCPHandler implements MCP tool handlers.
type MCPHandler struct {
	gc *GmailClient
}

// ---------------------------------------------------------------------------
// Tool arguments
//
// Every tool that names a message takes `id` — an opaque handle produced by
// gmail_search. Handles carry the mailbox as well as the message, so no tool
// assumes INBOX. See docs/adr/001-message-handles.md.
// ---------------------------------------------------------------------------

// SearchArgs are the arguments for gmail_search.
type SearchArgs struct {
	Query string `json:"query" jsonschema:"Gmail search query. Full Gmail syntax: is:unread, is:starred, from:alice@example.com, to:, subject:, label:work, has:attachment, newer_than:7d, after:2026/01/01, in:inbox, in:drafts, in:sent, in:trash, in:anywhere, plus free text. Combine freely, e.g. 'is:unread from:boss@co.com'. Defaults to in:inbox. Use in:drafts to list drafts."`
	Count int    `json:"count" jsonschema:"Maximum number of results (default: 20)"`
}

// ReadArgs are the arguments for gmail_read.
type ReadArgs struct {
	ID     string `json:"id" jsonschema:"Message id from gmail_search"`
	Format string `json:"format" jsonschema:"Which body to return: 'text' (default), 'html', or 'both'. Use 'both' before editing a draft so formatting is preserved."`
}

// ComposeArgs are the arguments for gmail_send and gmail_save_draft.
type ComposeArgs struct {
	From        string   `json:"from" jsonschema:"Sender address (optional; defaults to the connected account. Use for Gmail 'Send mail as' aliases)"`
	To          []string `json:"to" jsonschema:"Recipient email addresses"`
	Cc          []string `json:"cc" jsonschema:"Cc email addresses (optional)"`
	Bcc         []string `json:"bcc" jsonschema:"Bcc email addresses (optional). Never appears in the headers recipients can see."`
	Subject     string   `json:"subject" jsonschema:"Email subject line"`
	Body        string   `json:"body" jsonschema:"Plain text body"`
	BodyHTML    string   `json:"body_html" jsonschema:"HTML body (optional). When set, the message is sent as multipart/alternative with both versions."`
	Attachments []string `json:"attachments" jsonschema:"Workspace-relative file paths to attach (optional). Use the path returned by gmail_download_attachment to forward a file on."`
}

// UpdateDraftArgs are the arguments for gmail_update_draft. Every field is
// optional: omitting one preserves the draft's existing value, so the body can
// be changed without knowing the recipient. Passing an empty value clears it.
type UpdateDraftArgs struct {
	ID       string    `json:"id" jsonschema:"Draft id from gmail_search with in:drafts"`
	From     *string   `json:"from,omitempty" jsonschema:"New sender address. Omit to keep the current one."`
	To       *[]string `json:"to,omitempty" jsonschema:"Replace the recipients. Omit to keep the current ones."`
	Cc       *[]string `json:"cc,omitempty" jsonschema:"Replace the Cc list. Omit to keep it."`
	Bcc      *[]string `json:"bcc,omitempty" jsonschema:"Replace the Bcc list. Omit to keep it."`
	Subject  *string   `json:"subject,omitempty" jsonschema:"Replace the subject. Omit to keep it."`
	Body     *string   `json:"body,omitempty" jsonschema:"Replace the plain text body. Omit to keep it."`
	BodyHTML *string   `json:"body_html,omitempty" jsonschema:"Replace the HTML body. Omit to keep it."`
}

// IDArgs are the arguments for tools that act on one message and take nothing else.
type IDArgs struct {
	ID string `json:"id" jsonschema:"Message id from gmail_search"`
}

// ReplyArgs are the arguments for gmail_reply and gmail_draft_reply.
type ReplyArgs struct {
	ID       string `json:"id" jsonschema:"Id of the message being replied to"`
	Body     string `json:"body" jsonschema:"Plain text reply body"`
	BodyHTML string `json:"body_html" jsonschema:"HTML reply body (optional)"`
	ReplyAll bool   `json:"reply_all" jsonschema:"Also address everyone on the original's To and Cc (default: false, reply to sender only)"`
	From     string `json:"from" jsonschema:"Sender address (optional; defaults to the connected account)"`
}

// ForwardArgs are the arguments for gmail_forward.
type ForwardArgs struct {
	ID             string   `json:"id" jsonschema:"Id of the message to forward"`
	To             []string `json:"to" jsonschema:"Recipient email addresses"`
	AdditionalBody string   `json:"additional_body" jsonschema:"Text to place above the forwarded message (optional)"`
	From           string   `json:"from" jsonschema:"Sender address (optional; defaults to the connected account)"`
}

// SetFlagsArgs are the arguments for gmail_set_flags. Omitted fields are left alone.
type SetFlagsArgs struct {
	ID      string `json:"id" jsonschema:"Message id from gmail_search"`
	Read    *bool  `json:"read,omitempty" jsonschema:"true marks read, false marks unread. Omit to leave unchanged."`
	Starred *bool  `json:"starred,omitempty" jsonschema:"true stars, false unstars. Omit to leave unchanged."`
}

// LabelsArgs are the arguments for gmail_labels.
type LabelsArgs struct {
	ID     string   `json:"id" jsonschema:"Message id from gmail_search"`
	Add    []string `json:"add" jsonschema:"Labels to add. A message keeps its existing labels, so this files it without moving it. System labels use a backslash: \\Inbox, \\Starred, \\Important."`
	Remove []string `json:"remove" jsonschema:"Labels to remove. Remove \\Inbox to archive."`
}

// AttachmentArgs are the arguments for gmail_download_attachment.
type AttachmentArgs struct {
	ID       string `json:"id" jsonschema:"Id of the message holding the attachment"`
	Filename string `json:"filename" jsonschema:"Attachment filename, as listed by gmail_read"`
}

// NoArgs is for tools taking no parameters.
type NoArgs struct{}

// Tool annotation presets: hints MCP clients use to gate confirmation prompts.
var (
	falseVal    = false
	trueVal     = true
	annReadOnly = &mcp.ToolAnnotations{ReadOnlyHint: true}
	// Creates or transmits; never removes anything.
	annAdditive = &mcp.ToolAnnotations{DestructiveHint: &falseVal}
	// Toggles state in place; safe to repeat.
	annFlag = &mcp.ToolAnnotations{DestructiveHint: &falseVal, IdempotentHint: true}
	// Removes or relocates mail. Recoverable (Trash) but user-visible.
	annDestructive = &mcp.ToolAnnotations{DestructiveHint: &trueVal, IdempotentHint: true}
)

// RunMCPServer starts the MCP server using stdio transport.
func RunMCPServer() {
	gmailClient, err := NewGmailClient()
	if err != nil {
		log.Printf("Warning: Gmail client init failed (tools will return errors): %v", err)
		gmailClient = &GmailClient{settings: DefaultEmailSettings(), special: map[string]string{}}
	}

	handler := NewMCPHandler(gmailClient)
	gmailClient.TestConnection()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "oido-gmail",
		Version: "2.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_search",
		Description: "Search or list messages anywhere in the mailbox using Gmail search syntax. Returns ids used by every other tool, plus sender, recipient, date and whether the message has attachments. Use 'in:drafts' to list drafts, 'in:sent' for sent mail, 'in:inbox' (the default) for the inbox.",
		Annotations: annReadOnly,
	}, handler.HandleSearch)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_read",
		Description: "Read one message in full — headers (including To, Cc and Reply-To), body and attachment list. Works for any message: inbox, sent, archived, trashed or draft.",
		Annotations: annReadOnly,
	}, handler.HandleRead)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_list_labels",
		Description: "List the labels/mailboxes this account exposes.",
		Annotations: annReadOnly,
	}, handler.HandleListLabels)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_send",
		Description: "Send an email immediately. Requires send permission. To prepare a message for a human to review instead, use gmail_save_draft.",
		Annotations: annAdditive,
	}, handler.HandleSend)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_save_draft",
		Description: "Save a new draft without sending it. Returns the draft's id.",
		Annotations: annAdditive,
	}, handler.HandleSaveDraft)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_update_draft",
		Description: "Edit an existing draft. Only the fields you supply change; everything else is preserved — so you can rewrite the body without knowing the recipient. Returns a NEW id; the old one stops working.",
		Annotations: annAdditive,
	}, handler.HandleUpdateDraft)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_delete_draft",
		Description: "Discard a draft.",
		Annotations: annDestructive,
	}, handler.HandleDeleteDraft)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_send_draft",
		Description: "Send an existing draft and remove it from Drafts. Requires send permission.",
		Annotations: annAdditive,
	}, handler.HandleSendDraft)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_reply",
		Description: "Reply to a message and send it immediately. Set reply_all to include everyone on the original. Requires send permission.",
		Annotations: annAdditive,
	}, handler.HandleReply)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_draft_reply",
		Description: "Compose a reply and save it as a draft instead of sending. Does not require send permission.",
		Annotations: annAdditive,
	}, handler.HandleDraftReply)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_forward",
		Description: "Forward a message, carrying its attachments. Requires send permission.",
		Annotations: annAdditive,
	}, handler.HandleForward)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_set_flags",
		Description: "Mark a message read/unread and/or starred/unstarred. Omitted fields are left unchanged.",
		Annotations: annFlag,
	}, handler.HandleSetFlags)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_labels",
		Description: "Add or remove Gmail labels on a message. Adding a label files the message without moving it — a message can hold several labels at once. Remove \\Inbox to archive.",
		Annotations: annFlag,
	}, handler.HandleLabels)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_trash",
		Description: "Move a message to Trash from whichever mailbox it is in. Gmail purges Trash after 30 days, so this is recoverable until then.",
		Annotations: annDestructive,
	}, handler.HandleTrash)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_download_attachment",
		Description: "Save an attachment into the workspace and return its path. The file's bytes are written to disk, not returned, so large attachments do not flood the conversation. Pass the returned path to gmail_send to forward the file on.",
		Annotations: annReadOnly,
	}, handler.HandleDownloadAttachment)

	ctx := context.Background()
	log.Println("Oido Gmail MCP Server starting on stdio...")
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("MCP server error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// HandleSearch lists or searches messages.
func (h *MCPHandler) HandleSearch(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
	emails, err := h.gc.Search(ctx, args.Query, args.Count)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if len(emails) == 0 {
		return mcpOK("No messages matched."), nil, nil
	}
	return mcpOK(fmt.Sprintf("%d message(s):\n\n%s", len(emails), emailTable(emails))), nil, nil
}

// HandleRead reads one message in full.
func (h *MCPHandler) HandleRead(ctx context.Context, req *mcp.CallToolRequest, args ReadArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	d, err := h.gc.Read(ctx, handle, args.Format)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Id: %s\nFrom: %s\nTo: %s\n", d.ID, d.From, d.To)
	if d.Cc != "" {
		fmt.Fprintf(&b, "Cc: %s\n", d.Cc)
	}
	if d.ReplyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\n", d.ReplyTo)
	}
	fmt.Fprintf(&b, "Date: %s\nSubject: %s\n", d.Date, d.Subject)
	if len(d.Attachments) > 0 {
		b.WriteString("Attachments:\n")
		for _, a := range d.Attachments {
			fmt.Fprintf(&b, "  - %s (%s, %d bytes)\n", a.Filename, a.ContentType, a.Size)
		}
	}
	if d.BodyText != "" {
		fmt.Fprintf(&b, "\n--- text ---\n%s\n", d.BodyText)
	}
	if d.BodyHTML != "" {
		fmt.Fprintf(&b, "\n--- html ---\n%s\n", d.BodyHTML)
	}
	if d.BodyText == "" && d.BodyHTML == "" {
		b.WriteString("\n(no body)\n")
	}
	return mcpOK(b.String()), nil, nil
}

// HandleListLabels lists mailboxes/labels.
func (h *MCPHandler) HandleListLabels(ctx context.Context, req *mcp.CallToolRequest, args NoArgs) (*mcp.CallToolResult, any, error) {
	labels, err := h.gc.ListLabels(ctx)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(fmt.Sprintf("Labels (%d):\n%s", len(labels), strings.Join(labels, "\n"))), nil, nil
}

// composeFrom builds an OutgoingMessage from compose arguments.
func composeFrom(args ComposeArgs) (*OutgoingMessage, error) {
	atts, err := resolveOutgoingAttachments(args.Attachments)
	if err != nil {
		return nil, err
	}
	return &OutgoingMessage{
		From:        args.From,
		To:          args.To,
		Cc:          args.Cc,
		Bcc:         args.Bcc,
		Subject:     args.Subject,
		TextBody:    args.Body,
		HTMLBody:    args.BodyHTML,
		Attachments: atts,
	}, nil
}

// HandleSend sends an email.
func (h *MCPHandler) HandleSend(ctx context.Context, req *mcp.CallToolRequest, args ComposeArgs) (*mcp.CallToolResult, any, error) {
	m, err := composeFrom(args)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if err := h.gc.Send(ctx, m); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(fmt.Sprintf("Sent to %s.", strings.Join(args.To, ", "))), nil, nil
}

// HandleSaveDraft saves a new draft.
func (h *MCPHandler) HandleSaveDraft(ctx context.Context, req *mcp.CallToolRequest, args ComposeArgs) (*mcp.CallToolResult, any, error) {
	m, err := composeFrom(args)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	handle, err := h.gc.SaveDraft(ctx, m)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(draftSaved(handle)), nil, nil
}

// HandleUpdateDraft applies a partial update to an existing draft.
func (h *MCPHandler) HandleUpdateDraft(ctx context.Context, req *mcp.CallToolRequest, args UpdateDraftArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	newHandle, err := h.gc.UpdateDraft(ctx, handle, DraftPatch{
		From:     args.From,
		To:       args.To,
		Cc:       args.Cc,
		Bcc:      args.Bcc,
		Subject:  args.Subject,
		BodyText: args.Body,
		BodyHTML: args.BodyHTML,
	})
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK("Draft updated. " + draftSaved(newHandle) + "\nThe previous id is no longer valid."), nil, nil
}

// draftSaved renders the id of a saved draft, or explains its absence.
func draftSaved(h Handle) string {
	if h.UID == 0 {
		return "Draft saved, but its id could not be resolved — find it with gmail_search using in:drafts."
	}
	return "Draft id: " + h.String()
}

// HandleDeleteDraft discards a draft.
func (h *MCPHandler) HandleDeleteDraft(ctx context.Context, req *mcp.CallToolRequest, args IDArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if err := h.gc.DeleteDraft(ctx, handle); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK("Draft discarded."), nil, nil
}

// HandleSendDraft sends an existing draft.
func (h *MCPHandler) HandleSendDraft(ctx context.Context, req *mcp.CallToolRequest, args IDArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if err := h.gc.SendDraft(ctx, handle); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK("Draft sent and removed from Drafts."), nil, nil
}

// HandleReply sends a reply.
func (h *MCPHandler) HandleReply(ctx context.Context, req *mcp.CallToolRequest, args ReplyArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if err := h.gc.Reply(ctx, handle, args.From, args.Body, args.BodyHTML, args.ReplyAll); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK("Reply sent."), nil, nil
}

// HandleDraftReply saves a reply as a draft.
func (h *MCPHandler) HandleDraftReply(ctx context.Context, req *mcp.CallToolRequest, args ReplyArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	newHandle, err := h.gc.DraftReply(ctx, handle, args.From, args.Body, args.BodyHTML, args.ReplyAll)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK("Reply saved as a draft. " + draftSaved(newHandle)), nil, nil
}

// HandleForward forwards a message.
func (h *MCPHandler) HandleForward(ctx context.Context, req *mcp.CallToolRequest, args ForwardArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if err := h.gc.Forward(ctx, handle, args.From, args.To, args.AdditionalBody); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(fmt.Sprintf("Forwarded to %s.", strings.Join(args.To, ", "))), nil, nil
}

// HandleSetFlags changes read/starred state.
func (h *MCPHandler) HandleSetFlags(ctx context.Context, req *mcp.CallToolRequest, args SetFlagsArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if err := h.gc.SetFlags(ctx, handle, args.Read, args.Starred); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	var changed []string
	if args.Read != nil {
		changed = append(changed, map[bool]string{true: "read", false: "unread"}[*args.Read])
	}
	if args.Starred != nil {
		changed = append(changed, map[bool]string{true: "starred", false: "unstarred"}[*args.Starred])
	}
	return mcpOK("Marked " + strings.Join(changed, " and ") + "."), nil, nil
}

// HandleLabels adds and removes labels.
func (h *MCPHandler) HandleLabels(ctx context.Context, req *mcp.CallToolRequest, args LabelsArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if err := h.gc.Labels(ctx, handle, args.Add, args.Remove); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	var parts []string
	if len(args.Add) > 0 {
		parts = append(parts, "added "+strings.Join(args.Add, ", "))
	}
	if len(args.Remove) > 0 {
		parts = append(parts, "removed "+strings.Join(args.Remove, ", "))
	}
	return mcpOK("Labels " + strings.Join(parts, "; ") + "."), nil, nil
}

// HandleTrash moves a message to Trash.
func (h *MCPHandler) HandleTrash(ctx context.Context, req *mcp.CallToolRequest, args IDArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if err := h.gc.Trash(ctx, handle); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK("Moved to Trash. Recoverable for 30 days."), nil, nil
}

// HandleDownloadAttachment saves an attachment to the workspace.
func (h *MCPHandler) HandleDownloadAttachment(ctx context.Context, req *mcp.CallToolRequest, args AttachmentArgs) (*mcp.CallToolResult, any, error) {
	handle, err := ParseHandle(args.ID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if args.Filename == "" {
		return mcpErr("filename is required (gmail_read lists the attachments on a message)"), nil, nil
	}
	att, err := h.gc.DownloadAttachment(ctx, handle, args.Filename)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	out, _ := json.MarshalIndent(att, "", "  ")
	return mcpOK(string(out)), nil, nil
}

// ---------------------------------------------------------------------------
// Result helpers
// ---------------------------------------------------------------------------

// mcpOK returns a successful MCP tool result with the given text.
func mcpOK(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// mcpErr returns an error MCP tool result with the given message.
func mcpErr(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "Error: " + msg}},
		IsError: true,
	}
}

// emailTable renders message summaries. Ids go on their own line because they
// are long and must be copied verbatim into the next tool call.
func emailTable(emails []EmailSummary) string {
	var b strings.Builder
	for _, e := range emails {
		status := "unread"
		if e.Seen {
			status = "read"
		}
		marks := status
		if e.Starred {
			marks += ", starred"
		}
		if e.HasAttachments {
			marks += ", has attachments"
		}
		fmt.Fprintf(&b, "%s\n  From: %s\n", truncate(e.Subject, 78), truncate(e.From, 60))
		if e.To != "" {
			fmt.Fprintf(&b, "  To: %s\n", truncate(e.To, 60))
		}
		fmt.Fprintf(&b, "  Date: %s (%s)\n  Id: %s\n\n", e.Date, marks, e.ID)
	}
	return b.String()
}

// truncate shortens s to maxLen characters, appending an ellipsis when cut.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
