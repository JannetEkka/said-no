// Package saidno holds Said No's policy logic: the rule types, the validator
// that holds every rule to the user's own words, and the matcher that decides
// whether a tool call may run. It has no dependency on Bifrost, so it can be
// tested on its own; the plugin in package main wires it into Bifrost's hooks.
package saidno

import (
	"strings"
)

// Effect says whether a rule forbids or permits the tools it covers.
type Effect string

const (
	// Deny forbids the covered tool calls.
	Deny Effect = "deny"
	// Allow permits the covered tool calls, lifting an earlier Deny.
	Allow Effect = "allow"
)

// Op is a comparison between a tool-call argument and a value.
type Op string

// The allowed operators. Anything else is rejected by the validator.
const (
	OpAny         Op = "any"
	OpEquals      Op = "equals"
	OpContains    Op = "contains"
	OpNotContains Op = "not_contains"
	OpPrefix      Op = "prefix"
	OpSuffix      Op = "suffix"
)

var allowedOps = map[Op]bool{
	OpAny: true, OpEquals: true, OpContains: true, OpNotContains: true, OpPrefix: true, OpSuffix: true,
}

// Cond narrows a rule to calls whose argument Arg compares true with Value.
type Cond struct {
	Arg   string `json:"arg"`
	Op    Op     `json:"op"`
	Value string `json:"value"`
}

// Rule is one refusal or permission the user stated, in a fixed shape.
// Tools holds full tool names as the model sees them, or "*" for every
// tool that isn't read-only. Quote must be the user's own words.
type Rule struct {
	Effect       Effect   `json:"effect"`
	Tools        []string `json:"tools"`
	Where        []Cond   `json:"where,omitempty"`
	Quote        string   `json:"quote"`
	MessageIndex int      `json:"message_index"`
}

// Rejected records a rule the validator dropped and why, so it can be logged.
type Rejected struct {
	Rule   Rule   `json:"rule"`
	Reason string `json:"reason"`
}

// normalise lowercases s and collapses runs of whitespace, so a quote still
// matches when the model changes only case or spacing.
func normalise(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// sameTool reports whether a rule's tool name refers to the called tool.
// Bifrost prefixes MCP tools with their client name ("decoy-send_email"),
// so a bare name matches when it equals the part after the client prefix.
func sameTool(ruleTool, called, client string) bool {
	if ruleTool == called {
		return true
	}
	bare := called
	if client != "" {
		bare = strings.TrimPrefix(called, client+"-")
	}
	return ruleTool == bare || strings.TrimPrefix(ruleTool, client+"-") == bare
}
