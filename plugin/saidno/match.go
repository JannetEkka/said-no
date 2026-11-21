package saidno

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Call is one tool call the agent asked for, as Bifrost passes it to PreMCPHook.
type Call struct {
	Tool      string // full name, e.g. "decoy-send_email"
	Client    string // MCP client name, e.g. "decoy"
	Arguments string // the raw JSON arguments string
}

// Decision is the matcher's verdict on one call.
type Decision struct {
	Allowed  bool   `json:"allowed"`
	ReadOnly bool   `json:"read_only"`
	Rule     *Rule  `json:"rule,omitempty"`
	Reason   string `json:"reason"`
}

// Policy is the fixed part of the configuration the matcher needs.
type Policy struct {
	// ReadOnlyTools are glob patterns (path.Match syntax) for tools that only
	// read. They are never blocked, so the agent can always look things up.
	ReadOnlyTools []string
	// FailClosed blocks every non-read tool when the user's refusals could not
	// be read (extractor error or malformed reply). It is the default.
	FailClosed bool
}

// IsReadOnly reports whether the tool matches one of the read-only patterns.
func (p Policy) IsReadOnly(c Call) bool {
	bare := strings.TrimPrefix(c.Tool, c.Client+"-")
	for _, pat := range p.ReadOnlyTools {
		if ok, _ := path.Match(pat, c.Tool); ok {
			return true
		}
		if ok, _ := path.Match(pat, bare); ok {
			return true
		}
	}
	return false
}

// Decide rules on one call. Code decides, never a model: it reads only the
// validated rules, which came only from the user's own messages. Among rules
// that cover the call, the latest user message wins; at the same message a
// deny beats an allow. readFailed says the extractor failed for this
// conversation, in which case a fail-closed policy blocks non-read tools.
func (p Policy) Decide(rules []Rule, c Call, readFailed bool) Decision {
	if p.IsReadOnly(c) {
		return Decision{Allowed: true, ReadOnly: true, Reason: "read-only tool"}
	}
	args := parseArgs(c.Arguments)
	var winner *Rule
	for i := range rules {
		r := &rules[i]
		if !covers(r, c, args) {
			continue
		}
		if winner == nil || r.MessageIndex > winner.MessageIndex ||
			(r.MessageIndex == winner.MessageIndex && r.Effect == Deny) {
			winner = r
		}
	}
	if winner != nil && winner.Effect == Deny {
		return Decision{Allowed: false, Rule: winner, Reason: fmt.Sprintf("the user said %q", winner.Quote)}
	}
	if readFailed && p.FailClosed {
		return Decision{Allowed: false, Rule: winner, Reason: "the user's refusals could not be read, so actions are blocked until they can be"}
	}
	if winner != nil {
		return Decision{Allowed: true, Rule: winner, Reason: fmt.Sprintf("the user allowed it: %q", winner.Quote)}
	}
	return Decision{Allowed: true, Reason: "no refusal covers this call"}
}

// covers reports whether rule r applies to call c with parsed arguments args.
func covers(r *Rule, c Call, args map[string]string) bool {
	toolHit := false
	for _, t := range r.Tools {
		if t == "*" || sameTool(t, c.Tool, c.Client) {
			toolHit = true
			break
		}
	}
	if !toolHit {
		return false
	}
	for _, cond := range r.Where {
		if !holds(cond, args) {
			return false
		}
	}
	return true
}

// holds evaluates one condition. A missing argument makes it false, except for
// not_contains, where an absent value can't contain the forbidden text.
func holds(c Cond, args map[string]string) bool {
	if c.Op == OpAny {
		return true
	}
	if c.Arg == "*" {
		// Any argument: used for the scope named in a permission.
		for _, v := range args {
			if holds(Cond{Arg: "x", Op: c.Op, Value: c.Value}, map[string]string{"x": v}) {
				return true
			}
		}
		return c.Op == OpNotContains && len(args) == 0
	}
	v, ok := args[strings.ToLower(c.Arg)]
	want := strings.ToLower(c.Value)
	if !ok {
		return c.Op == OpNotContains
	}
	v = strings.ToLower(v)
	switch c.Op {
	case OpEquals:
		return v == want
	case OpContains:
		return strings.Contains(v, want)
	case OpNotContains:
		return !strings.Contains(v, want)
	case OpPrefix:
		return strings.HasPrefix(v, want)
	case OpSuffix:
		return strings.HasSuffix(v, want)
	}
	return false
}

// parseArgs flattens a JSON arguments object into lowercase keys and string
// values. Arrays are joined with ", "; nested objects are kept as JSON.
// Unparseable arguments give an empty map, so conditions on them are false.
func parseArgs(raw string) map[string]string {
	out := map[string]string{}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return out
	}
	for k, v := range m {
		out[strings.ToLower(k)] = stringify(v)
	}
	return out
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, stringify(e))
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
