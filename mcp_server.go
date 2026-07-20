package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"

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

// ListEmailsArgs represents the arguments for list_emails tool.
type ListEmailsArgs struct {
	Count int `json:"count" jsonschema:"Number of recent emails to retrieve (default: 20)"`
}

// ReadEmailArgs represents the arguments for read_email tool.
type ReadEmailArgs struct {
	UID uint32 `json:"uid" jsonschema:"Unique ID of the email to read"`
}

// SendEmailArgs represents the arguments for send_email tool.
type SendEmailArgs struct {
	From    string `json:"from" jsonschema:"Sender address (optional, defaults to GMAIL_EMAIL; use for Gmail 'Send mail as' aliases)"`
	To      string `json:"to" jsonschema:"Recipient email address"`
	Subject string `json:"subject" jsonschema:"Email subject line"`
	Body    string `json:"body" jsonschema:"Email body text"`
}

// SaveDraftArgs represents the arguments for save_draft tool.
type SaveDraftArgs struct {
	From    string `json:"from" jsonschema:"Sender address (optional, defaults to GMAIL_EMAIL; use for Gmail 'Send mail as' aliases)"`
	To      string `json:"to" jsonschema:"Recipient email address"`
	Subject string `json:"subject" jsonschema:"Email subject line"`
	Body    string `json:"body" jsonschema:"Email body text"`
}

// SearchEmailsArgs represents the arguments for search_emails tool.
type SearchEmailsArgs struct {
	Query string `json:"query" jsonschema:"Gmail search query. Supports full Gmail syntax: is:unread, is:starred, from:alice@example.com, to:, subject:, label:work, has:attachment, newer_than:7d, after:2026/01/01, plus free text. Operators can be combined, e.g. 'is:unread from:boss@co.com'"`
	Count int    `json:"count" jsonschema:"Maximum number of results to return (default: 20)"`
}

// DeleteEmailArgs represents the arguments for gmail_delete_email tool.
type DeleteEmailArgs struct {
	UID uint32 `json:"uid" jsonschema:"UID of the email to delete (move to Trash)"`
}

// MarkEmailArgs represents the arguments for gmail_mark_read and gmail_mark_unread tools.
type MarkEmailArgs struct {
	UID    uint32 `json:"uid" jsonschema:"UID of the email to mark"`
	Folder string `json:"folder" jsonschema:"Mailbox folder containing the email (default: INBOX)"`
}

// ListLabelsArgs represents the arguments for gmail_list_labels tool (no parameters).
type ListLabelsArgs struct{}

// MoveEmailArgs represents the arguments for gmail_move_email tool.
type MoveEmailArgs struct {
	UID       uint32 `json:"uid" jsonschema:"UID of the email to move"`
	SrcFolder string `json:"src_folder" jsonschema:"Source mailbox folder (default: INBOX)"`
	DstFolder string `json:"dst_folder" jsonschema:"Destination mailbox folder (e.g. [Gmail]/Archive)"`
}

// ReplyEmailArgs represents the arguments for gmail_reply_email tool.
type ReplyEmailArgs struct {
	From string `json:"from" jsonschema:"Sender address (optional, defaults to GMAIL_EMAIL; use for Gmail 'Send mail as' aliases)"`
	UID  uint32 `json:"uid" jsonschema:"UID of the email to reply to"`
	Body string `json:"body" jsonschema:"Reply body text"`
}

// ForwardEmailArgs represents the arguments for gmail_forward_email tool.
type ForwardEmailArgs struct {
	From           string `json:"from" jsonschema:"Sender address (optional, defaults to GMAIL_EMAIL; use for Gmail 'Send mail as' aliases)"`
	UID            uint32 `json:"uid" jsonschema:"UID of the email to forward"`
	To             string `json:"to" jsonschema:"Recipient email address"`
	AdditionalBody string `json:"additional_body" jsonschema:"Optional text to prepend before the forwarded message"`
}

// ListDraftsArgs represents the arguments for gmail_list_drafts tool.
type ListDraftsArgs struct {
	Count int `json:"count" jsonschema:"Number of recent drafts to retrieve (default: 20)"`
}

// StarEmailArgs represents the arguments for gmail_star_email and gmail_unstar_email tools.
type StarEmailArgs struct {
	UID uint32 `json:"uid" jsonschema:"UID of the email to star or unstar"`
}

// GetAttachmentsArgs represents the arguments for gmail_get_attachments tool.
type GetAttachmentsArgs struct {
	UID uint32 `json:"uid" jsonschema:"UID of the email to inspect for attachments"`
}

// DownloadAttachmentArgs represents the arguments for gmail_download_attachment tool.
type DownloadAttachmentArgs struct {
	UID      uint32 `json:"uid" jsonschema:"UID of the email containing the attachment"`
	Filename string `json:"filename" jsonschema:"Name of the attachment to download"`
}

// EmptyTrashArgs represents the arguments for gmail_empty_trash tool (no parameters).
type EmptyTrashArgs struct{}

// Tool annotation presets: hints MCP clients use to gate confirmation prompts.
var (
	falseVal       = false
	trueVal        = true
	annReadOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true}
	annAdditive    = &mcp.ToolAnnotations{DestructiveHint: &falseVal}                       // sends/creates mail, never removes
	annFlag        = &mcp.ToolAnnotations{DestructiveHint: &falseVal, IdempotentHint: true} // toggles flags, safe to repeat
	annDestructive = &mcp.ToolAnnotations{DestructiveHint: &trueVal, IdempotentHint: true}  // deletes or moves mail
)

// RunMCPServer starts the MCP server using stdio transport.
func RunMCPServer() {
	gmailClient, err := NewGmailClient()
	if err != nil {
		log.Printf("Warning: Gmail client init failed (tools will return errors): %v", err)
		gmailClient = &GmailClient{settings: DefaultEmailSettings()}
	}

	handler := NewMCPHandler(gmailClient)

	gmailClient.TestConnection()

	// Create MCP server
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "oido-gmail",
		Version: "1.0.0",
	}, nil)

	// Register gmail_list_emails tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_list_emails",
		Description: "List recent emails from the INBOX. Returns subject, from, date, and UID for each email.",
		Annotations: annReadOnly,
	}, handler.HandleListEmails)

	// Register gmail_read_email tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_read_email",
		Description: "Read the full content of a specific email by UID. Returns subject, from, to, date, and body.",
		Annotations: annReadOnly,
	}, handler.HandleReadEmail)

	// Register gmail_send_email tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_send_email",
		Description: "Send an email via SMTP. Requires recipient address, subject, and body.",
		Annotations: annAdditive,
	}, handler.HandleSendEmail)

	// Register gmail_save_draft tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_save_draft",
		Description: "Save an email as a draft in [Gmail]/Drafts. Requires recipient address, subject, and body. Requires GMAIL_ALLOW_SEND=true.",
		Annotations: annAdditive,
	}, handler.HandleSaveDraft)

	// Register gmail_search_emails tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_search_emails",
		Description: "Search all mail (inbox, sent, and archived) using full Gmail search syntax (is:unread, in:sent, from:, to:, subject:, label:, has:attachment, newer_than:7d, free text). Returns matching emails with subject, from, date, read status, and UID.",
		Annotations: annReadOnly,
	}, handler.HandleSearchEmails)

	// Register gmail_delete_email tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_delete_email",
		Description: "Move an email to [Gmail]/Trash by UID. Requires GMAIL_ALLOW_SEND=true.",
		Annotations: annDestructive,
	}, handler.HandleDeleteEmail)

	// Register gmail_mark_read tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_mark_read",
		Description: "Mark an email as read by UID. Optionally specify the folder (default: INBOX).",
		Annotations: annFlag,
	}, handler.HandleMarkRead)

	// Register gmail_mark_unread tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_mark_unread",
		Description: "Mark an email as unread by UID. Optionally specify the folder (default: INBOX).",
		Annotations: annFlag,
	}, handler.HandleMarkUnread)

	// Register gmail_list_labels tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_list_labels",
		Description: "List all available Gmail labels and folders (IMAP mailboxes).",
		Annotations: annReadOnly,
	}, handler.HandleListLabels)

	// Register gmail_move_email tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_move_email",
		Description: "Move an email from one folder to another by UID. Requires GMAIL_ALLOW_SEND=true.",
		Annotations: annDestructive,
	}, handler.HandleMoveEmail)

	// Register gmail_reply_email tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_reply_email",
		Description: "Send a reply to an email by UID with proper In-Reply-To headers. Requires GMAIL_ALLOW_SEND=true.",
		Annotations: annAdditive,
	}, handler.HandleReplyEmail)

	// Register gmail_forward_email tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_forward_email",
		Description: "Forward an email to a recipient with the original message quoted. Requires GMAIL_ALLOW_SEND=true.",
		Annotations: annAdditive,
	}, handler.HandleForwardEmail)

	// Register gmail_list_drafts tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_list_drafts",
		Description: "List recent drafts from [Gmail]/Drafts. Returns subject, from, date, and UID.",
		Annotations: annReadOnly,
	}, handler.HandleListDrafts)

	// Register gmail_star_email tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_star_email",
		Description: "Star an email by UID (adds the Flagged flag in INBOX).",
		Annotations: annFlag,
	}, handler.HandleStarEmail)

	// Register gmail_unstar_email tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_unstar_email",
		Description: "Remove the star from an email by UID (removes the Flagged flag in INBOX).",
		Annotations: annFlag,
	}, handler.HandleUnstarEmail)

	// Register gmail_get_attachments tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_get_attachments",
		Description: "List all attachments in an email by UID. Returns filename, content type, and size.",
		Annotations: annReadOnly,
	}, handler.HandleGetAttachments)

	// Register gmail_download_attachment tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_download_attachment",
		Description: "Download a named attachment from an email by UID. Returns base64-encoded content.",
		Annotations: annReadOnly,
	}, handler.HandleDownloadAttachment)

	// Register gmail_empty_trash tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_empty_trash",
		Description: "Permanently delete all emails in [Gmail]/Trash. Requires GMAIL_ALLOW_SEND=true.",
		Annotations: annDestructive,
	}, handler.HandleEmptyTrash)

	// Run server using stdio transport
	ctx := context.Background()
	log.Println("Oido Gmail MCP Server starting on stdio...")

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("MCP server error: %v", err)
	}
}

// HandleListEmails lists recent emails.
func (h *MCPHandler) HandleListEmails(ctx context.Context, req *mcp.CallToolRequest, args ListEmailsArgs) (*mcp.CallToolResult, any, error) {
	emails, err := h.gc.ListEmails(ctx, args.Count)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Error: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	if len(emails) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "No emails found in INBOX."},
			},
		}, nil, nil
	}

	var result string
	result += fmt.Sprintf("Recent Emails (%d):\n\n", len(emails))
	result += emailTable(emails)

	result += "\nUse gmail_read_email with a UID to view full message content."

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, nil, nil
}

// HandleReadEmail reads a specific email.
func (h *MCPHandler) HandleReadEmail(ctx context.Context, req *mcp.CallToolRequest, args ReadEmailArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Error: uid parameter is required"},
			},
			IsError: true,
		}, nil, nil
	}

	email, err := h.gc.ReadEmail(ctx, args.UID)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Error: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	var result string
	result += fmt.Sprintf("Subject: %s\n", email.Subject)
	result += fmt.Sprintf("From:    %s\n", email.From)
	result += fmt.Sprintf("To:      %s\n", email.To)
	result += fmt.Sprintf("Date:    %s\n", email.Date)
	result += "-------\n"
	result += email.Body

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, nil, nil
}

// HandleSendEmail sends an email.
func (h *MCPHandler) HandleSendEmail(ctx context.Context, req *mcp.CallToolRequest, args SendEmailArgs) (*mcp.CallToolResult, any, error) {
	if args.To == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Error: to parameter is required (recipient email address)"},
			},
			IsError: true,
		}, nil, nil
	}

	if args.Subject == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Error: subject parameter is required"},
			},
			IsError: true,
		}, nil, nil
	}

	if args.Body == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Error: body parameter is required"},
			},
			IsError: true,
		}, nil, nil
	}

	err := h.gc.SendEmail(ctx, args.From, args.To, args.Subject, args.Body)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Error: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("Email sent successfully to %s", args.To)},
		},
	}, nil, nil
}

// HandleSaveDraft saves an email as a draft.
func (h *MCPHandler) HandleSaveDraft(ctx context.Context, req *mcp.CallToolRequest, args SaveDraftArgs) (*mcp.CallToolResult, any, error) {
	if args.To == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Error: to parameter is required (recipient email address)"},
			},
			IsError: true,
		}, nil, nil
	}

	if args.Subject == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Error: subject parameter is required"},
			},
			IsError: true,
		}, nil, nil
	}

	if args.Body == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Error: body parameter is required"},
			},
			IsError: true,
		}, nil, nil
	}

	err := h.gc.SaveDraft(ctx, args.From, args.To, args.Subject, args.Body)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Error: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("Draft saved successfully for %s", args.To)},
		},
	}, nil, nil
}

// HandleSearchEmails searches emails.
func (h *MCPHandler) HandleSearchEmails(ctx context.Context, req *mcp.CallToolRequest, args SearchEmailsArgs) (*mcp.CallToolResult, any, error) {
	if args.Query == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Error: query parameter is required"},
			},
			IsError: true,
		}, nil, nil
	}

	emails, err := h.gc.SearchEmails(ctx, args.Query, args.Count)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Error: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	if len(emails) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("No emails found matching query: %s", args.Query)},
			},
		}, nil, nil
	}

	var result string
	result += fmt.Sprintf("Search Results for \"%s\" (%d):\n\n", args.Query, len(emails))
	result += emailTable(emails)

	result += "\nUse gmail_read_email with a UID to view full message content."

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, nil, nil
}

// HandleDeleteEmail moves an email to Trash.
func (h *MCPHandler) HandleDeleteEmail(ctx context.Context, req *mcp.CallToolRequest, args DeleteEmailArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return mcpErr("uid parameter is required"), nil, nil
	}
	if err := h.gc.DeleteEmail(ctx, args.UID); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(fmt.Sprintf("Email UID %d moved to Trash.", args.UID)), nil, nil
}

// HandleMarkRead marks an email as read.
func (h *MCPHandler) HandleMarkRead(ctx context.Context, req *mcp.CallToolRequest, args MarkEmailArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return mcpErr("uid parameter is required"), nil, nil
	}
	if err := h.gc.MarkEmail(ctx, args.UID, args.Folder, true); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(fmt.Sprintf("Email UID %d marked as read.", args.UID)), nil, nil
}

// HandleMarkUnread marks an email as unread.
func (h *MCPHandler) HandleMarkUnread(ctx context.Context, req *mcp.CallToolRequest, args MarkEmailArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return mcpErr("uid parameter is required"), nil, nil
	}
	if err := h.gc.MarkEmail(ctx, args.UID, args.Folder, false); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(fmt.Sprintf("Email UID %d marked as unread.", args.UID)), nil, nil
}

// HandleListLabels lists all Gmail labels/folders.
func (h *MCPHandler) HandleListLabels(ctx context.Context, req *mcp.CallToolRequest, args ListLabelsArgs) (*mcp.CallToolResult, any, error) {
	labels, err := h.gc.ListLabels(ctx)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if len(labels) == 0 {
		return mcpOK("No labels found."), nil, nil
	}
	result := fmt.Sprintf("Labels (%d):\n", len(labels))
	for _, l := range labels {
		result += fmt.Sprintf("  - %s\n", l)
	}
	return mcpOK(result), nil, nil
}

// HandleMoveEmail moves an email to a different folder.
func (h *MCPHandler) HandleMoveEmail(ctx context.Context, req *mcp.CallToolRequest, args MoveEmailArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return mcpErr("uid parameter is required"), nil, nil
	}
	if args.DstFolder == "" {
		return mcpErr("dst_folder parameter is required"), nil, nil
	}
	if err := h.gc.MoveEmail(ctx, args.UID, args.SrcFolder, args.DstFolder); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	src := args.SrcFolder
	if src == "" {
		src = "INBOX"
	}
	return mcpOK(fmt.Sprintf("Email UID %d moved from %s to %s.", args.UID, src, args.DstFolder)), nil, nil
}

// HandleReplyEmail sends a reply to an email.
func (h *MCPHandler) HandleReplyEmail(ctx context.Context, req *mcp.CallToolRequest, args ReplyEmailArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return mcpErr("uid parameter is required"), nil, nil
	}
	if args.Body == "" {
		return mcpErr("body parameter is required"), nil, nil
	}
	if err := h.gc.ReplyEmail(ctx, args.From, args.UID, args.Body); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(fmt.Sprintf("Reply to email UID %d sent.", args.UID)), nil, nil
}

// HandleForwardEmail forwards an email to a recipient.
func (h *MCPHandler) HandleForwardEmail(ctx context.Context, req *mcp.CallToolRequest, args ForwardEmailArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return mcpErr("uid parameter is required"), nil, nil
	}
	if args.To == "" {
		return mcpErr("to parameter is required"), nil, nil
	}
	if err := h.gc.ForwardEmail(ctx, args.From, args.UID, args.To, args.AdditionalBody); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(fmt.Sprintf("Email UID %d forwarded to %s.", args.UID, args.To)), nil, nil
}

// HandleListDrafts lists recent drafts.
func (h *MCPHandler) HandleListDrafts(ctx context.Context, req *mcp.CallToolRequest, args ListDraftsArgs) (*mcp.CallToolResult, any, error) {
	drafts, err := h.gc.ListDrafts(ctx, args.Count)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if len(drafts) == 0 {
		return mcpOK("No drafts found."), nil, nil
	}
	result := fmt.Sprintf("Drafts (%d):\n\n", len(drafts))
	result += "UID    | From                        | Date                        | Subject\n"
	result += "-------+-----------------------------+-----------------------------+---------------------------\n"
	for _, e := range drafts {
		result += fmt.Sprintf("%-6d | %-27s | %-27s | %s\n",
			e.UID, truncate(e.From, 27), truncate(e.Date, 27), truncate(e.Subject, 25))
	}
	return mcpOK(result), nil, nil
}

// HandleStarEmail stars an email.
func (h *MCPHandler) HandleStarEmail(ctx context.Context, req *mcp.CallToolRequest, args StarEmailArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return mcpErr("uid parameter is required"), nil, nil
	}
	if err := h.gc.StarEmail(ctx, args.UID, true); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(fmt.Sprintf("Email UID %d starred.", args.UID)), nil, nil
}

// HandleUnstarEmail removes the star from an email.
func (h *MCPHandler) HandleUnstarEmail(ctx context.Context, req *mcp.CallToolRequest, args StarEmailArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return mcpErr("uid parameter is required"), nil, nil
	}
	if err := h.gc.StarEmail(ctx, args.UID, false); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK(fmt.Sprintf("Email UID %d unstarred.", args.UID)), nil, nil
}

// HandleGetAttachments lists attachments on an email.
func (h *MCPHandler) HandleGetAttachments(ctx context.Context, req *mcp.CallToolRequest, args GetAttachmentsArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return mcpErr("uid parameter is required"), nil, nil
	}
	attachments, err := h.gc.GetAttachments(ctx, args.UID)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if len(attachments) == 0 {
		return mcpOK(fmt.Sprintf("No attachments found in email UID %d.", args.UID)), nil, nil
	}
	result := fmt.Sprintf("Attachments in email UID %d (%d):\n\n", args.UID, len(attachments))
	result += "Filename                     | Content-Type                 | Size\n"
	result += "-----------------------------+------------------------------+----------\n"
	for _, a := range attachments {
		result += fmt.Sprintf("%-27s | %-28s | %d bytes\n",
			truncate(a.Filename, 27), truncate(a.ContentType, 28), a.Size)
	}
	result += "\nUse gmail_download_attachment with uid and filename to retrieve the file."
	return mcpOK(result), nil, nil
}

// HandleDownloadAttachment downloads an attachment and returns it base64-encoded.
func (h *MCPHandler) HandleDownloadAttachment(ctx context.Context, req *mcp.CallToolRequest, args DownloadAttachmentArgs) (*mcp.CallToolResult, any, error) {
	if args.UID == 0 {
		return mcpErr("uid parameter is required"), nil, nil
	}
	if args.Filename == "" {
		return mcpErr("filename parameter is required"), nil, nil
	}
	att, err := h.gc.DownloadAttachment(ctx, args.UID, args.Filename)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	result := fmt.Sprintf("Attachment: %s\nContent-Type: %s\nSize: %d bytes\nEncoding: base64\n\n%s",
		att.Filename, att.ContentType, len(att.Data), base64.StdEncoding.EncodeToString(att.Data))
	return mcpOK(result), nil, nil
}

// HandleEmptyTrash permanently deletes all emails in Trash.
func (h *MCPHandler) HandleEmptyTrash(ctx context.Context, req *mcp.CallToolRequest, args EmptyTrashArgs) (*mcp.CallToolResult, any, error) {
	if err := h.gc.EmptyTrash(ctx); err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	return mcpOK("Trash emptied successfully."), nil, nil
}

// mcpOK returns a successful MCP tool result with the given text.
func mcpOK(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// mcpErr returns an error MCP tool result with the given message.
func mcpErr(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "Error: " + msg}},
		IsError: true,
	}
}

// emailTable renders email summaries as a fixed-width table with read status.
func emailTable(emails []EmailSummary) string {
	result := "UID    | Status | From                        | Date                        | Subject\n"
	result += "-------+--------+-----------------------------+-----------------------------+---------------------------\n"
	for _, e := range emails {
		status := "unread"
		if e.Seen {
			status = "read"
		}
		result += fmt.Sprintf("%-6d | %-6s | %-27s | %-27s | %s\n",
			e.UID, status, truncate(e.From, 27), truncate(e.Date, 27), truncate(e.Subject, 25))
	}
	return result
}

// truncate shortens a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
