package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TypesafeHandler wraps the Gmail client with a TypeSafe client, giving the
// tools in this file access to both without touching MCPHandler's shape.
type TypesafeHandler struct {
	gc *GmailClient
	ts *typesafeClient
}

// maxIDsPerCall bounds gmail_classify/gmail_suggest_labels/gmail_phishing_check,
// each of which does one full gc.Read per id before the (single) TypeSafe call.
const maxIDsPerCall = 50

var importanceCriteria = []string{
	"not important: promotional, automated notification, or no action possible",
	"low: informational, no action needed soon",
	"medium: worth reading this week, minor action possible",
	"high: needs a timely response or decision",
	"critical: urgent, time-sensitive, or from someone the user must not ignore",
}

// maxImportanceScore is the top of the importanceCriteria scale, derived so
// the display denominator can never drift out of sync with the criteria list.
func maxImportanceScore() float64 {
	return float64(len(importanceCriteria) - 1)
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

// truncateRunes cuts s to at most max bytes without splitting a multi-byte
// UTF-8 rune, so excerpts sent to TypeSafe never end in a corrupted character.
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

func checkIDsBound(ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("ids is required (get them from gmail_search or gmail_triage)")
	}
	if len(ids) > maxIDsPerCall {
		return fmt.Errorf("too many ids (%d): pass at most %d per call, each one costs a full message read", len(ids), maxIDsPerCall)
	}
	return nil
}

// TriageArgs are the arguments for gmail_triage.
type TriageArgs struct {
	Query string `json:"query" jsonschema:"Gmail search query, same syntax as gmail_search. Defaults to 'is:unread in:inbox'."`
	Count int    `json:"count" jsonschema:"Maximum messages to consider (default 20, max 50)."`
}

// rankedEmail is one triaged message: its importance score and whether it
// likely needs a reply, pulled out of the raw TypeSafe answers map.
type rankedEmail struct {
	e          EmailSummary
	score      float64
	needsReply bool
}

// rankByImportance pairs each email with its TypeSafe answers and sorts by
// importance score descending. A missing answer (e.g. a partial API response)
// defaults to score 0 / no reply needed rather than panicking or vanishing.
func rankByImportance(emails []EmailSummary, answers map[string]typesafeAnswer) []rankedEmail {
	rows := make([]rankedEmail, 0, len(emails))
	for _, e := range emails {
		r := rankedEmail{e: e}
		if a, ok := answers["importance_"+e.ID]; ok && a.Score != nil {
			r.score = *a.Score
		}
		if a, ok := answers["reply_"+e.ID]; ok && a.Noul != nil {
			r.needsReply = *a.Noul >= 0.5
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].score > rows[j].score })
	return rows
}

func formatTriage(rows []rankedEmail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d message(s), ranked by importance:\n\n", len(rows))
	for _, r := range rows {
		reply := ""
		if r.needsReply {
			reply = " [needs reply]"
		}
		fmt.Fprintf(&b, "- (%.1f/%.0f)%s %s — from %s — %s — id %s\n", r.score, maxImportanceScore(), reply, r.e.Subject, r.e.From, r.e.Date, r.e.ID)
	}
	return b.String()
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

	return mcpOK(formatTriage(rankByImportance(emails, answers))), nil, nil
}

// ClassifyArgs are the arguments for gmail_classify.
type ClassifyArgs struct {
	IDs        []string          `json:"ids" jsonschema:"Message ids to classify, from gmail_search or gmail_triage. Max 50 per call."`
	Categories map[string]string `json:"categories" jsonschema:"Map of category name to a short rubric describing when it applies, e.g. {\"urgent\": \"needs action today\", \"newsletter\": \"bulk/marketing content\"}. 2-255 categories."`
}

func (h *TypesafeHandler) HandleClassify(ctx context.Context, req *mcp.CallToolRequest, args ClassifyArgs) (*mcp.CallToolResult, any, error) {
	if err := checkIDsBound(args.IDs); err != nil {
		return mcpErr(err.Error()), nil, nil
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
	IDs   []string `json:"ids" jsonschema:"Message ids to label, from gmail_search or gmail_triage. Kept explicit and bounded (max 50) since each id costs one full read."`
	Apply bool     `json:"apply" jsonschema:"If true, add the suggested label to each message (requires GMAIL_ALLOW_ORGANIZE). If false (default), only suggest."`
}

const noLabelOption = "no_label"

// labelCandidate is one message read in full for label suggestion: kept as a
// typed struct (not a map[string]any) so its id is used as a Handle directly,
// with no type assertion needed to apply the chosen label back.
type labelCandidate struct {
	handle  Handle
	id      string
	from    string
	subject string
	body    string
}

func (h *TypesafeHandler) HandleSuggestLabels(ctx context.Context, req *mcp.CallToolRequest, args SuggestLabelsArgs) (*mcp.CallToolResult, any, error) {
	if err := checkIDsBound(args.IDs); err != nil {
		return mcpErr(err.Error()), nil, nil
	}

	labels, err := h.gc.ListLabels(ctx)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}
	criteria := map[string]string{noLabelOption: "none of the existing labels fit well"}
	for _, l := range labels {
		criteria[l] = "the message belongs under this existing label"
	}

	candidates := make([]labelCandidate, 0, len(args.IDs))
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
		c := labelCandidate{handle: handle, id: idStr, from: d.From, subject: d.Subject, body: truncateRunes(d.BodyText, 1000)}
		candidates = append(candidates, c)
		state = append(state, map[string]any{"id": c.id, "from": c.from, "subject": c.subject, "body_excerpt": c.body})
	}

	questions := make(map[string]typesafeQuestion, len(candidates))
	for _, c := range candidates {
		questions["label_"+c.id] = typesafeQuestion{
			Type:         "choice",
			Instructions: fmt.Sprintf("Which existing label best fits the email with id %q? Choose %q if none fit well.", c.id, noLabelOption),
			Criteria:     criteria,
		}
	}

	answers, err := h.ts.ask(ctx, state, questions)
	if err != nil {
		return mcpErr(err.Error()), nil, nil
	}

	var b strings.Builder
	for _, c := range candidates {
		a := answers["label_"+c.id]
		line := fmt.Sprintf("- %s -> %s", c.id, a.Choice)
		if args.Apply && a.Choice != noLabelOption {
			if err := h.gc.Labels(ctx, c.handle, []string{a.Choice}, nil); err != nil {
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
	IDs []string `json:"ids" jsonschema:"Message ids to check, from gmail_search or gmail_triage. Max 50 per call."`
}

func (h *TypesafeHandler) HandlePhishingCheck(ctx context.Context, req *mcp.CallToolRequest, args PhishingCheckArgs) (*mcp.CallToolResult, any, error) {
	if err := checkIDsBound(args.IDs); err != nil {
		return mcpErr(err.Error()), nil, nil
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
		body := truncateRunes(d.BodyText, 1500)
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

// digestCategoryOrder is the preferred print order for known categories.
// otherCategory catches anything TypeSafe returns outside that set (an
// unanswered question, a hallucinated label, a future extra category) so no
// message is ever silently dropped from the digest.
var digestCategoryOrder = []string{"action_required", "fyi", "notification", "newsletter"}

const otherCategory = "other"

// groupForDigest buckets each email under its TypeSafe category, falling back
// to otherCategory for anything not in digestCategoryOrder, and returns the
// print order: known categories first (in digestCategoryOrder), then any
// other categories actually seen, in first-seen order, so the total message
// count printed always matches len(emails).
func groupForDigest(emails []EmailSummary, answers map[string]typesafeAnswer) (groups map[string][]string, order []string) {
	groups = map[string][]string{}
	known := map[string]bool{}
	for _, c := range digestCategoryOrder {
		known[c] = true
	}

	var extra []string
	seenExtra := map[string]bool{}
	for _, e := range emails {
		cat := answers["cat_"+e.ID].Choice
		if cat == "" || !known[cat] {
			if cat == "" {
				cat = otherCategory
			}
			if !known[cat] && !seenExtra[cat] {
				seenExtra[cat] = true
				extra = append(extra, cat)
			}
		}

		score := 0.0
		if a, ok := answers["importance_"+e.ID]; ok && a.Score != nil {
			score = *a.Score
		}
		reply := ""
		if a, ok := answers["reply_"+e.ID]; ok && a.Noul != nil && *a.Noul >= 0.5 {
			reply = " [needs reply]"
		}
		line := fmt.Sprintf("  - (%.1f/%.0f)%s %s — from %s — id %s", score, maxImportanceScore(), reply, e.Subject, e.From, e.ID)
		groups[cat] = append(groups[cat], line)
	}

	order = append(append([]string{}, digestCategoryOrder...), extra...)
	return groups, order
}

func formatDigest(total int, groups map[string][]string, order []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Digest of %d message(s):\n\n", total)
	for _, cat := range order {
		lines, ok := groups[cat]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%s (%d):\n%s\n\n", cat, len(lines), strings.Join(lines, "\n"))
	}
	return b.String()
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

	groups, order := groupForDigest(emails, answers)
	return mcpOK(formatDigest(len(emails), groups, order)), nil, nil
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
