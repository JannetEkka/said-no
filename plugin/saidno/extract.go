package saidno

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// cue matches language that can state a refusal or a permission. Messages
// without any cue skip the model entirely: most user turns ask for work and
// refuse nothing, so this saves a model call per turn.
var cue = regexp.MustCompile(`(?i)\b(don'?t|do not|never|no|not|stop|without|avoid|refrain|unless|only|okay|ok|go ahead|you (can|may)|allowed?|permission|instead|hold off|wait)\b`)

// HasCue reports whether a user message might state a refusal or permission.
func HasCue(message string) bool {
	return cue.MatchString(message)
}

// Tool is one entry in the tool catalog shown to the extractor.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Extractor turns user sentences into rule JSON with a model. It describes;
// it never decides. Its output goes through Validate before it counts.
type Extractor struct {
	URL       string            // OpenAI-compatible chat completions URL
	Model     string            // e.g. "gai/gemini-3.5-flash-lite"
	Headers   map[string]string // extra headers, e.g. the virtual key and the internal marker
	Fallbacks []string          // models Bifrost tries if Model is overloaded (Bifrost's "fallbacks" field)
	Client    *http.Client
}

const systemPrompt = `You read what a user told an AI assistant and write down every refusal or permission about ACTIONS (tool calls) as JSON rules. You do not judge whether anything is safe; you only transcribe what the user said.

Return exactly: {"rules": [ ... ]} where each rule is
{"effect": "deny" | "allow",
 "tools": [tool names copied exactly from the TOOLS list, or "*" for every action tool],
 "where": [{"arg": "<argument name from the tool's parameters>", "op": "equals" | "contains" | "not_contains" | "prefix" | "suffix" | "any", "value": "<text>"}],
 "quote": "<the user's exact words, copied character for character from the CURRENT message>"}

Rules:
- Only the CURRENT message creates rules. EARLIER messages are context only (for example to know who "her" is).
- "deny": the user forbids an action ("don't email anyone", "never delete files in /contracts", "do not post this yet").
- "allow": the user now permits an action, often lifting an earlier refusal ("ok, you can email Priya now").
- Use "where" to narrow a rule to what the user named (a recipient, a folder, a channel). Leave "where" empty when the user meant every use of the tool. Use argument names from the tool's parameters.
- "quote" must be copied exactly from the CURRENT message. Never paraphrase.
- A request to do something ("email the summary to Priya") is an allow for that action only if the user explicitly grants permission; a plain task request is NOT a rule.
- If the message refuses or permits nothing about actions, return {"rules": []}.
- If a refusal names no tool you can map ("don't do anything risky"), return no rule for it.`

// userPrompt builds the user turn: the tool catalog, earlier user messages, and the current one.
func userPrompt(current string, earlier []string, tools []Tool) string {
	var b strings.Builder
	b.WriteString("TOOLS:\n")
	for _, t := range tools {
		fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Description)
	}
	b.WriteString("\nEARLIER USER MESSAGES (context only):\n")
	if len(earlier) == 0 {
		b.WriteString("(none)\n")
	}
	for i, m := range earlier {
		fmt.Fprintf(&b, "[%d] %s\n", i, m)
	}
	b.WriteString("\nCURRENT MESSAGE:\n")
	b.WriteString(current)
	return b.String()
}

// Extract asks the model for rules stated in current. An error means the
// refusals could not be read; the caller treats that as an unknown state.
func (e *Extractor) Extract(ctx context.Context, current string, earlier []string, tools []Tool) ([]Rule, error) {
	body := map[string]any{
		"model":           e.Model,
		"temperature":     0,
		"response_format": map[string]string{"type": "json_object"},
		"fallbacks":       e.Fallbacks,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt(current, earlier, tools)},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.Headers {
		req.Header.Set(k, v)
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("extractor request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("extractor status %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	return ParseCompletion(data)
}

// ParseCompletion reads rules out of an OpenAI-style chat completion body.
// A missing or malformed "rules" field is an error, never "no rules".
func ParseCompletion(data []byte) ([]Rule, error) {
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &completion); err != nil {
		return nil, fmt.Errorf("extractor reply is not JSON: %w", err)
	}
	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("extractor reply has no choices")
	}
	return ParseRules(completion.Choices[0].Message.Content)
}

// ParseRules reads {"rules": [...]} from the model's text, tolerating a
// Markdown code fence around it.
func ParseRules(content string) ([]Rule, error) {
	s := strings.TrimSpace(content)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	var out struct {
		Rules *[]Rule `json:"rules"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &out); err != nil {
		return nil, fmt.Errorf("rules are not valid JSON: %w", err)
	}
	if out.Rules == nil {
		return nil, fmt.Errorf(`reply has no "rules" field`)
	}
	return *out.Rules, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
