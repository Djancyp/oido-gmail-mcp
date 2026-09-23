package main

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func f64(v float64) *float64 { return &v }

func TestRankByImportanceSortsDescendingAndDefaultsMissingAnswers(t *testing.T) {
	emails := []EmailSummary{
		{ID: "a", Subject: "low"},
		{ID: "b", Subject: "high"},
		{ID: "c", Subject: "missing"}, // no answers at all
	}
	answers := map[string]typesafeAnswer{
		"importance_a": {Score: f64(1)},
		"reply_a":      {Noul: f64(0.1)},
		"importance_b": {Score: f64(4)},
		"reply_b":      {Noul: f64(0.9)},
	}

	rows := rankByImportance(emails, answers)

	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[0].e.ID != "b" || rows[1].e.ID != "a" || rows[2].e.ID != "c" {
		t.Fatalf("wrong order: %v, %v, %v", rows[0].e.ID, rows[1].e.ID, rows[2].e.ID)
	}
	if !rows[0].needsReply {
		t.Error("email b should need a reply (noul 0.9)")
	}
	if rows[1].needsReply {
		t.Error("email a should not need a reply (noul 0.1)")
	}
	if rows[2].score != 0 || rows[2].needsReply {
		t.Errorf("email with no answers should default to score=0, needsReply=false; got %+v", rows[2])
	}
}

func TestFormatTriageDenominatorMatchesCriteriaLength(t *testing.T) {
	rows := []rankedEmail{{e: EmailSummary{ID: "a", Subject: "s"}, score: 3}}
	out := formatTriage(rows)

	want := fmt.Sprintf("/%.0f)", maxImportanceScore())
	if !strings.Contains(out, want) {
		t.Errorf("expected denominator %q derived from importanceCriteria (len %d) in output, got: %s", want, len(importanceCriteria), out)
	}
}

func TestGroupForDigestNeverDropsAMessage(t *testing.T) {
	emails := []EmailSummary{
		{ID: "a", Subject: "known"},
		{ID: "b", Subject: "unknown-category"},
		{ID: "c", Subject: "empty-choice"},
	}
	answers := map[string]typesafeAnswer{
		"cat_a": {Choice: "action_required"},
		"cat_b": {Choice: "spam_probably"}, // not in digestCategoryOrder
		// cat_c intentionally missing -> Choice defaults to ""
	}

	groups, order := groupForDigest(emails, answers)

	total := 0
	for _, lines := range groups {
		total += len(lines)
	}
	if total != len(emails) {
		t.Fatalf("dropped messages: got %d lines across groups, want %d", total, len(emails))
	}

	if _, ok := groups["action_required"]; !ok {
		t.Error("expected known category action_required to be present")
	}
	if _, ok := groups["spam_probably"]; !ok {
		t.Error("expected unrecognized category to be kept under its own key, not dropped")
	}
	if _, ok := groups[otherCategory]; !ok {
		t.Error("expected empty/missing choice to fall back to otherCategory")
	}

	foundSpam, foundOther := false, false
	for _, c := range order {
		if c == "spam_probably" {
			foundSpam = true
		}
		if c == otherCategory {
			foundOther = true
		}
	}
	if !foundSpam || !foundOther {
		t.Errorf("print order must include every category actually used, got: %v", order)
	}
}

func TestFormatDigestPrintsEveryGroupInOrder(t *testing.T) {
	groups := map[string][]string{
		"action_required": {"  - line1"},
		"other":           {"  - line2"},
	}
	order := []string{"action_required", "fyi", "notification", "newsletter", "other"}

	out := formatDigest(2, groups, order)

	if !strings.Contains(out, "Digest of 2 message(s):") {
		t.Errorf("missing total count header, got: %s", out)
	}
	if !strings.Contains(out, "action_required (1):") || !strings.Contains(out, "other (1):") {
		t.Errorf("missing expected groups, got: %s", out)
	}
	if strings.Contains(out, "fyi (") || strings.Contains(out, "notification (") {
		t.Errorf("empty groups should not be printed, got: %s", out)
	}
}

func TestTruncateRunesNeverSplitsAMultiByteRune(t *testing.T) {
	s := "hello wörld" // 'ö' is 2 bytes, sits right around common cut points
	for max := 0; max <= len(s)+2; max++ {
		out := truncateRunes(s, max)
		if !utf8.ValidString(out) {
			t.Fatalf("truncateRunes(%q, %d) produced invalid UTF-8: %q", s, max, out)
		}
	}
	if truncateRunes(s, 100) != s {
		t.Error("truncateRunes should return the full string when under the limit")
	}
}

func TestCheckIDsBound(t *testing.T) {
	if err := checkIDsBound(nil); err == nil {
		t.Error("expected error for empty ids")
	}
	ids := make([]string, maxIDsPerCall+1)
	if err := checkIDsBound(ids); err == nil {
		t.Error("expected error for too many ids")
	}
	if err := checkIDsBound(ids[:maxIDsPerCall]); err != nil {
		t.Errorf("expected exactly maxIDsPerCall ids to be allowed, got: %v", err)
	}
}
