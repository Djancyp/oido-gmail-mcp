package main

import (
	"context"
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
	To      string `json:"to" jsonschema:"Recipient email address"`
	Subject string `json:"subject" jsonschema:"Email subject line"`
	Body    string `json:"body" jsonschema:"Email body text"`
}

// SaveDraftArgs represents the arguments for save_draft tool.
type SaveDraftArgs struct {
	To      string `json:"to" jsonschema:"Recipient email address"`
	Subject string `json:"subject" jsonschema:"Email subject line"`
	Body    string `json:"body" jsonschema:"Email body text"`
}

// SearchEmailsArgs represents the arguments for search_emails tool.
type SearchEmailsArgs struct {
	Query string `json:"query" jsonschema:"Search term to match in email subject"`
	Count int    `json:"count" jsonschema:"Maximum number of results to return (default: 20)"`
}

// RunMCPServer starts the MCP server using stdio transport.
func RunMCPServer() {
	gmailClient, err := NewGmailClient()
	if err != nil {
		log.Fatalf("Failed to create Gmail client: %v", err)
	}

	handler := NewMCPHandler(gmailClient)

	// Create MCP server
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "oido-gmail",
		Version: "1.0.0",
	}, nil)

	// Register list_emails tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_emails",
		Description: "List recent emails from the INBOX. Returns subject, from, date, and UID for each email.",
	}, handler.HandleListEmails)

	// Register read_email tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_email",
		Description: "Read the full content of a specific email by UID. Returns subject, from, to, date, and body.",
	}, handler.HandleReadEmail)

	// Register send_email tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "send_email",
		Description: "Send an email via SMTP. Requires recipient address, subject, and body.",
	}, handler.HandleSendEmail)

	// Register save_draft tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "save_draft",
		Description: "Save an email as a draft in [Gmail]/Drafts. Requires recipient address, subject, and body. Requires GMAIL_ALLOW_SEND=true.",
	}, handler.HandleSaveDraft)

	// Register search_emails tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_emails",
		Description: "Search emails by subject in the INBOX. Returns matching emails with subject, from, date, and UID.",
	}, handler.HandleSearchEmails)

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
	result += "UID    | From                        | Date                        | Subject\n"
	result += "-------+-----------------------------+-----------------------------+---------------------------\n"

	for _, e := range emails {
		from := truncate(e.From, 27)
		date := truncate(e.Date, 27)
		subject := truncate(e.Subject, 25)
		result += fmt.Sprintf("%-6d | %-27s | %-27s | %s\n", e.UID, from, date, subject)
	}

	result += "\nUse read_email with a UID to view full message content."

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

	err := h.gc.SendEmail(ctx, args.To, args.Subject, args.Body)
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

	err := h.gc.SaveDraft(ctx, args.To, args.Subject, args.Body)
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
	result += "UID    | From                        | Date                        | Subject\n"
	result += "-------+-----------------------------+-----------------------------+---------------------------\n"

	for _, e := range emails {
		from := truncate(e.From, 27)
		date := truncate(e.Date, 27)
		subject := truncate(e.Subject, 25)
		result += fmt.Sprintf("%-6d | %-27s | %-27s | %s\n", e.UID, from, date, subject)
	}

	result += "\nUse read_email with a UID to view full message content."

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: result},
		},
	}, nil, nil
}

// truncate shortens a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
