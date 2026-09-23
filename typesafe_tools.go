package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TypesafeHandler wraps the Gmail client with a TypeSafe client, giving the
// tools in this file access to both without touching MCPHandler's shape.
type TypesafeHandler struct {
	gc *GmailClient
	ts *typesafeClient
}

var importanceCriteria = []string{
	"not important: promotional, automated notification, or no action possible",
	"low: informational, no action needed soon",
	"medium: worth reading this week, minor action possible",
	"high: needs a timely response or decision",
	"critical: urgent, time-sensitive, or from someone the user must not ignore",
}

// registerTypesafeTools adds Gmail+TypeSafe tools to server if TypeSafe is
// configured. Called from RunMCPServer; a no-op (nil cfg) leaves every other
// Gmail tool untouched.
func registerTypesafeTools(server *mcp.Server, gc *GmailClient, cfg *typesafeConfig) {
	if cfg == nil {
		return
	}
	h := &TypesafeHandler{gc: gc, ts: newTypesafeClient(cfg)}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_triage",
		Description: "Search mail and rank results by importance and reply-need using TypeSafe, in one pass. Use this instead of gmail_search when the user asks 'what's important' or 'do I need to reply to anything'. Scores subject/sender/date only — does not read message bodies.",
		Annotations: annReadOnly,
	}, h.HandleTriage)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_classify",
		Description: "Classify already-known message ids into caller-supplied categories using TypeSafe (e.g. urgent/newsletter/receipt/personal). Get ids from gmail_search or gmail_triage first.",
		Annotations: annReadOnly,
	}, h.HandleClassify)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_suggest_labels",
		Description: "Read given messages in full and suggest which existing Gmail label best fits each, using TypeSafe. Set apply=true to actually add the suggested label (requires GMAIL_ALLOW_ORGANIZE); otherwise only returns suggestions.",
		Annotations: annFlag,
	}, h.HandleSuggestLabels)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_phishing_check",
		Description: "Read given messages in full and flag likely phishing/social-engineering using TypeSafe. Advisory only — a high score means 'verify before trusting', never a reason to auto-delete or auto-block.",
		Annotations: annReadOnly,
	}, h.HandlePhishingCheck)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_digest",
		Description: "Search mail and produce a grouped digest (action_required / fyi / newsletter / notification) with importance and reply-need per message, using TypeSafe. Use for 'summarize my inbox' or 'what happened while I was away'.",
		Annotations: annReadOnly,
	}, h.HandleDigest)
}

// emailState is the shared JSON state given to TypeSafe for a batch of
// messages: enough for a judgment about importance/category without pulling
// full bodies (which would mean N extra IMAP round trips).
func emailState(emails []EmailSummary) []map[string]any {
	state := make([]map[string]any, len(emails))
	for i, e := range emails {
		state[i] = map[string]any{
			"id":              e.ID,
			"from":            e.From,
			"subject":         e.Subject,
			"date":            e.Date,
			"seen":            e.Seen,
			"has_attachments": e.HasAttachments,
		}
	}
	return state
}

// TriageArgs are the arguments for gmail_triage.
type TriageArgs struct {
	Query string `json:"query" jsonschema:"Gmail search query, same syntax as gmail_search. Defaults to 'is:unread in:inbox'."`
	Count int    `json:"count" jsonschema:"Maximum messages to consider (default 20, max 50)."`
}

func (h *TypesafeHandler) HandleTriage(ctx context.Context, req *mcp.CallToolRequest, args TriageArgs) (*mcp.CallToolResult, any, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		query = "is:unread in:inbox"
	}
	count := args.Count
	if count <= 0 || count > 50 {
		count = 20
	}

	emails, err := h.gc.Search(ctx, query, count)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if len(emails) == 0 {
		return mcpOK("No messages matched."), nil, nil
	}

	questions := make(map[string]typesafeQuestion, len(emails)*2)
	for _, e := range emails {
		questions["importance_"+e.ID] = typesafeQuestion{
			Type:         "score",
			Instructions: fmt.Sprintf("How important/urgent is the email with id %q, based on its subject and sender, for the mailbox owner?", e.ID),
			Criteria:     importanceCriteria,
		}
		questions["reply_"+e.ID] = typesafeQuestion{
			Type:         "noul",
			Instructions: fmt.Sprintf("Does the email with id %q likely require a reply from the mailbox owner?", e.ID),
		}
	}

	answers, err := h.ts.ask(ctx, emailState(emails), questions)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}

	type ranked struct {
		e          EmailSummary
		score      float64
		needsReply bool
	}
	rows := make([]ranked, 0, len(emails))
	for _, e := range emails {
		r := ranked{e: e}
		if a, ok := answers["importance_"+e.ID]; ok && a.Score != nil {
			r.score = *a.Score
		}
		if a, ok := answers["reply_"+e.ID]; ok && a.Noul != nil {
			r.needsReply = *a.Noul >= 0.5
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].score > rows[j].score })

	var b strings.Builder
	fmt.Fprintf(&b, "%d message(s), ranked by importance:\n\n", len(rows))
	for _, r := range rows {
		reply := ""
		if r.needsReply {
			reply = " [needs reply]"
		}
		fmt.Fprintf(&b, "- (%.1f/4)%s %s — from %s — %s — id %s\n", r.score, reply, r.e.Subject, r.e.From, r.e.Date, r.e.ID)
	}
	return mcpOK(b.String()), nil, nil
}

// ClassifyArgs are the arguments for gmail_classify.
type ClassifyArgs struct {
	IDs        []string          `json:"ids" jsonschema:"Message ids to classify, from gmail_search or gmail_triage."`
	Categories map[string]string `json:"categories" jsonschema:"Map of category name to a short rubric describing when it applies, e.g. {\"urgent\": \"needs action today\", \"newsletter\": \"bulk/marketing content\"}. 2-255 categories."`
}

func (h *TypesafeHandler) HandleClassify(ctx context.Context, req *mcp.CallToolRequest, args ClassifyArgs) (*mcp.CallToolResult, any, error) {
	if len(args.IDs) == 0 {
		return mcpErr("ids is required (get them from gmail_search or gmail_triage)"), nil, nil
	}
	if len(args.Categories) < 2 {
		return mcpErr("categories must list at least 2 options"), nil, nil
	}

	emails, err := summariesForIDs(ctx, h.gc, args.IDs)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}

	questions := make(map[string]typesafeQuestion, len(emails))
	for _, e := range emails {
		questions["cat_"+e.ID] = typesafeQuestion{
			Type:         "choice",
			Instructions: fmt.Sprintf("Which category best fits the email with id %q, based on its subject and sender?", e.ID),
			Criteria:     args.Categories,
		}
	}

	answers, err := h.ts.ask(ctx, emailState(emails), questions)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}

	var b strings.Builder
	for _, e := range emails {
		a := answers["cat_"+e.ID]
		fmt.Fprintf(&b, "- %s — %s — from %s — id %s\n", a.Choice, e.Subject, e.From, e.ID)
	}
	return mcpOK(b.String()), nil, nil
}

// SuggestLabelsArgs are the arguments for gmail_suggest_labels.
type SuggestLabelsArgs struct {
	IDs   []string `json:"ids" jsonschema:"Message ids to label, from gmail_search or gmail_triage. Kept explicit and bounded since each id costs one full read."`
	Apply bool     `json:"apply" jsonschema:"If true, add the suggested label to each message (requires GMAIL_ALLOW_ORGANIZE). If false (default), only suggest."`
}

const noLabelOption = "no_label"

func (h *TypesafeHandler) HandleSuggestLabels(ctx context.Context, req *mcp.CallToolRequest, args SuggestLabelsArgs) (*mcp.CallToolResult, any, error) {
	if len(args.IDs) == 0 {
		return mcpErr("ids is required (get them from gmail_search or gmail_triage)"), nil, nil
	}

	labels, err := h.gc.ListLabels(ctx)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	criteria := map[string]string{noLabelOption: "none of the existing labels fit well"}
	for _, l := range labels {
		criteria[l] = "the message belongs under this existing label"
	}

	type detail struct {
		id      Handle
		summary map[string]any
	}
	details := make([]detail, 0, len(args.IDs))
	state := make([]map[string]any, 0, len(args.IDs))
	for _, idStr := range args.IDs {
		handle, err := ParseHandle(idStr)
		if err != nil {
			return mcpErr(err.Error()), nil, nil
		}
		d, err := h.gc.Read(ctx, handle, "text")
		if err != nil {
			return mcpErr(fmt.Sprintf("reading %s: %v", idStr, err)), nil, nil
		}
		body := d.BodyText
		if len(body) > 1000 {
			body = body[:1000]
		}
		s := map[string]any{"id": idStr, "from": d.From, "subject": d.Subject, "body_excerpt": body}
		details = append(details, detail{id: handle, summary: s})
		state = append(state, s)
	}

	questions := make(map[string]typesafeQuestion, len(details))
	for _, d := range details {
		questions["label_"+d.summary["id"].(string)] = typesafeQuestion{
			Type:         "choice",
			Instructions: fmt.Sprintf("Which existing label best fits the email with id %q? Choose %q if none fit well.", d.summary["id"], noLabelOption),
			Criteria:     criteria,
		}
	}

	answers, err := h.ts.ask(ctx, state, questions)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}

	var b strings.Builder
	for _, d := range details {
		idStr := d.summary["id"].(string)
		a := answers["label_"+idStr]
		line := fmt.Sprintf("- %s -> %s", idStr, a.Choice)
		if args.Apply && a.Choice != noLabelOption {
			if err := h.gc.Labels(ctx, d.id, []string{a.Choice}, nil); err != nil {
				line += fmt.Sprintf(" (apply failed: %v)", err)
			} else {
				line += " (applied)"
			}
		}
		b.WriteString(line + "\n")
	}
	return mcpOK(b.String()), nil, nil
}

// PhishingCheckArgs are the arguments for gmail_phishing_check.
type PhishingCheckArgs struct {
	IDs []string `json:"ids" jsonschema:"Message ids to check, from gmail_search or gmail_triage."`
}

func (h *TypesafeHandler) HandlePhishingCheck(ctx context.Context, req *mcp.CallToolRequest, args PhishingCheckArgs) (*mcp.CallToolResult, any, error) {
	if len(args.IDs) == 0 {
		return mcpErr("ids is required"), nil, nil
	}

	type row struct {
		id, subject, from string
	}
	rows := make([]row, 0, len(args.IDs))
	state := make([]map[string]any, 0, len(args.IDs))
	for _, idStr := range args.IDs {
		handle, err := ParseHandle(idStr)
		if err != nil {
			return mcpErr(err.Error()), nil, nil
		}
		d, err := h.gc.Read(ctx, handle, "text")
		if err != nil {
			return mcpErr(fmt.Sprintf("reading %s: %v", idStr, err)), nil, nil
		}
		body := d.BodyText
		if len(body) > 1500 {
			body = body[:1500]
		}
		rows = append(rows, row{id: idStr, subject: d.Subject, from: d.From})
		state = append(state, map[string]any{
			"id": idStr, "from": d.From, "reply_to": d.ReplyTo, "subject": d.Subject, "body_excerpt": body,
		})
	}

	questions := make(map[string]typesafeQuestion, len(rows))
	for _, r := range rows {
		questions["phish_"+r.id] = typesafeQuestion{
			Type: "noul",
			Instructions: fmt.Sprintf(
				"Does the email with id %q show phishing or social-engineering indicators (urgency pressure, spoofed or mismatched sender/reply-to, suspicious links, requests for credentials or payment)?",
				r.id,
			),
		}
	}

	answers, err := h.ts.ask(ctx, state, questions)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}

	var b strings.Builder
	b.WriteString("Advisory only — verify before trusting; do not auto-delete based on this.\n\n")
	for _, r := range rows {
		a := answers["phish_"+r.id]
		p := 0.0
		if a.Noul != nil {
			p = *a.Noul
		}
		flag := ""
		if p >= 0.5 {
			flag = " [SUSPICIOUS]"
		}
		fmt.Fprintf(&b, "- %.2f%s %s — from %s — id %s\n", p, flag, r.subject, r.from, r.id)
	}
	return mcpOK(b.String()), nil, nil
}

// DigestArgs are the arguments for gmail_digest.
type DigestArgs struct {
	Query string `json:"query" jsonschema:"Gmail search query, same syntax as gmail_search. Defaults to 'is:unread in:inbox'."`
	Count int    `json:"count" jsonschema:"Maximum messages to consider (default 20, max 50)."`
}

var digestCategories = map[string]string{
	"action_required": "needs a decision, reply, or task from the user",
	"fyi":             "informational, no action needed",
	"newsletter":      "bulk/marketing/subscription content",
	"notification":    "automated system or service notification",
}

func (h *TypesafeHandler) HandleDigest(ctx context.Context, req *mcp.CallToolRequest, args DigestArgs) (*mcp.CallToolResult, any, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		query = "is:unread in:inbox"
	}
	count := args.Count
	if count <= 0 || count > 50 {
		count = 20
	}

	emails, err := h.gc.Search(ctx, query, count)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	if len(emails) == 0 {
		return mcpOK("No messages matched."), nil, nil
	}

	questions := make(map[string]typesafeQuestion, len(emails)*3)
	for _, e := range emails {
		questions["importance_"+e.ID] = typesafeQuestion{
			Type:         "score",
			Instructions: fmt.Sprintf("How important/urgent is the email with id %q, based on its subject and sender?", e.ID),
			Criteria:     importanceCriteria,
		}
		questions["reply_"+e.ID] = typesafeQuestion{
			Type:         "noul",
			Instructions: fmt.Sprintf("Does the email with id %q likely require a reply from the mailbox owner?", e.ID),
		}
		questions["cat_"+e.ID] = typesafeQuestion{
			Type:         "choice",
			Instructions: fmt.Sprintf("Which category best fits the email with id %q?", e.ID),
			Criteria:     digestCategories,
		}
	}

	answers, err := h.ts.ask(ctx, emailState(emails), questions)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}

	groups := map[string][]string{}
	for _, e := range emails {
		cat := answers["cat_"+e.ID].Choice
		score := 0.0
		if a, ok := answers["importance_"+e.ID]; ok && a.Score != nil {
			score = *a.Score
		}
		reply := ""
		if a, ok := answers["reply_"+e.ID]; ok && a.Noul != nil && *a.Noul >= 0.5 {
			reply = " [needs reply]"
		}
		line := fmt.Sprintf("  - (%.1f/4)%s %s — from %s — id %s", score, reply, e.Subject, e.From, e.ID)
		groups[cat] = append(groups[cat], line)
	}

	order := []string{"action_required", "fyi", "notification", "newsletter"}
	var b strings.Builder
	fmt.Fprintf(&b, "Digest of %d message(s):\n\n", len(emails))
	for _, cat := range order {
		lines, ok := groups[cat]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%s (%d):\n%s\n\n", cat, len(lines), strings.Join(lines, "\n"))
	}
	return mcpOK(b.String()), nil, nil
}

// summariesForIDs resolves explicit message ids into EmailSummary-shaped data
// via gmail_read, since IDs may come from any earlier search/triage call and
// we don't keep a cache of prior EmailSummary results.
func summariesForIDs(ctx context.Context, gc *GmailClient, ids []string) ([]EmailSummary, error) {
	out := make([]EmailSummary, 0, len(ids))
	for _, idStr := range ids {
		handle, err := ParseHandle(idStr)
		if err != nil {
			return nil, err
		}
		d, err := gc.Read(ctx, handle, "text")
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", idStr, err)
		}
		out = append(out, EmailSummary{ID: idStr, Subject: d.Subject, From: d.From, Date: d.Date})
	}
	return out, nil
}
