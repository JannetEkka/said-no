package saidno

import (
	"fmt"
	"strings"
)

// Validate checks rules proposed by the extractor for user message msgIndex
// (whose text is message) against the live tool catalog. It returns the rules
// that pass, with MessageIndex set by code, and the ones it dropped.
//
// A rule survives only if its effect and operators are known, every tool is in
// the catalog (or is "*"), and its quote appears in the user's message. A rule
// that can't point at the user's own words doesn't exist. An empty catalog
// skips the tool check, because some requests don't carry a tool list.
func Validate(proposed []Rule, msgIndex int, message string, catalog []string) ([]Rule, []Rejected) {
	var kept []Rule
	var dropped []Rejected
	known := make(map[string]bool, len(catalog))
	for _, t := range catalog {
		known[t] = true
	}
	for _, r := range proposed {
		if reason := checkRule(r, message, known); reason != "" {
			dropped = append(dropped, Rejected{Rule: r, Reason: reason})
			continue
		}
		r, reason := narrowToQuote(r)
		if reason != "" {
			dropped = append(dropped, Rejected{Rule: r, Reason: reason})
			continue
		}
		r.MessageIndex = msgIndex
		kept = append(kept, r)
	}
	return kept, dropped
}

// checkRule returns why r is invalid, or "" if it is valid.
func checkRule(r Rule, message string, known map[string]bool) string {
	if r.Effect != Deny && r.Effect != Allow {
		return fmt.Sprintf("unknown effect %q", r.Effect)
	}
	if len(r.Tools) == 0 {
		return "no tools named"
	}
	for _, t := range r.Tools {
		if t == "*" || len(known) == 0 {
			continue
		}
		if !inCatalog(t, known) {
			return fmt.Sprintf("tool %q is not in the tool list", t)
		}
	}
	for _, c := range r.Where {
		if !allowedOps[c.Op] {
			return fmt.Sprintf("operator %q is not allowed", c.Op)
		}
		if c.Op != OpAny && (c.Arg == "" || c.Value == "") {
			return "condition needs an argument and a value"
		}
	}
	q := normalise(r.Quote)
	if q == "" {
		return "no quote"
	}
	if !containsNormalised(message, q) {
		return fmt.Sprintf("quote %q is not in the user's message", r.Quote)
	}
	return ""
}

// narrowToQuote holds a rule's conditions to the user's words, like its quote.
// A condition narrows what a rule covers, so it must come from the sentence
// that states the rule. For a deny, a condition whose value isn't in the quote
// is removed: the refusal then covers the whole tool, the safe direction. For
// an allow, such a condition means the permission isn't what the user said,
// so the whole rule is rejected.
func narrowToQuote(r Rule) (Rule, string) {
	q := normalise(r.Quote)
	var where []Cond
	for _, c := range r.Where {
		if c.Op == OpAny || strings.Contains(q, normalise(c.Value)) {
			where = append(where, c)
			continue
		}
		if r.Effect == Allow {
			return r, fmt.Sprintf("permission narrowed by %q, which is not in the user's words", c.Value)
		}
	}
	r.Where = where
	return r, ""
}

// inCatalog accepts a tool by its full name or by its bare name after a client prefix.
func inCatalog(t string, known map[string]bool) bool {
	if known[t] {
		return true
	}
	for name := range known {
		if sameTool(t, name, clientOf(name)) {
			return true
		}
	}
	return false
}

// clientOf returns the client prefix of a Bifrost MCP tool name ("decoy" for "decoy-send_email").
func clientOf(name string) string {
	if i := strings.Index(name, "-"); i > 0 {
		return name[:i]
	}
	return ""
}

// containsNormalised reports whether the normalised quote occurs in the normalised message.
func containsNormalised(message, normalisedQuote string) bool {
	return len(normalisedQuote) > 0 && strings.Contains(normalise(message), normalisedQuote)
}
