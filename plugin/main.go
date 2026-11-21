// Command said-no is a Bifrost plugin, built with -buildmode=plugin, that
// holds an agent to the user's own refusals. It reads refusals and
// permissions only from user messages (PreLLMHook), turns them into
// validated rules, and blocks any MCP tool call a refusal still covers
// (PreMCPHook), quoting the user's words. See README.md.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JannetEkka/said-no/plugin/saidno"
	"github.com/maximhq/bifrost/core/schemas"
)

// Config is the plugin's "config" object in Bifrost's config.json.
type Config struct {
	// ExtractorURL is an OpenAI-compatible chat completions URL. The default
	// sends the extractor's call back through this Bifrost, so it is logged,
	// budgeted and routed like any other call.
	ExtractorURL string `json:"extractor_url"`
	// ExtractorModel is the model for refusal extraction, e.g. "gai/gemini-3.5-flash-lite".
	ExtractorModel string `json:"extractor_model"`
	// ExtractorHeaders are sent with each extractor call (for example x-bf-vk).
	ExtractorHeaders map[string]string `json:"extractor_headers"`
	// ReadOnlyTools are glob patterns for tools that are never blocked.
	ReadOnlyTools []string `json:"read_only_tools"`
	// FailClosed blocks non-read tools when refusals could not be read. Default true.
	FailClosed *bool `json:"fail_closed"`
	// BlockUnknownConversations blocks non-read tool calls that can't be tied to
	// a conversation (for example /v1/mcp/tool/execute without the session
	// header). Default true.
	BlockUnknownConversations *bool `json:"block_unknown_conversations"`
	// SessionHeader names the conversation. Default "x-said-no-session".
	SessionHeader string `json:"session_header"`
	// DecisionLog is a JSONL file that receives every decision. Optional.
	DecisionLog string `json:"decision_log"`
}

// internalHeader marks the extractor's own calls so the plugin skips them.
const internalHeader = "x-said-no-internal"

type ctxKey string

const sessionKey ctxKey = "said-no-session"

var (
	cfg     Config
	policy  saidno.Policy
	store   *saidno.Store
	logMu   sync.Mutex
	logFile *os.File

	catalogMu sync.Mutex
	catalog   = map[string]saidno.Tool{}
)

// Init reads the plugin config and sets defaults.
func Init(config any) error {
	raw, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("said-no: config: %w", err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("said-no: config: %w", err)
	}
	if cfg.ExtractorURL == "" {
		cfg.ExtractorURL = "http://127.0.0.1:8080/v1/chat/completions"
	}
	if cfg.SessionHeader == "" {
		cfg.SessionHeader = "x-said-no-session"
	}
	if cfg.ExtractorModel == "" {
		return fmt.Errorf("said-no: extractor_model is required")
	}
	headers := map[string]string{internalHeader: "1"}
	for k, v := range cfg.ExtractorHeaders {
		headers[k] = os.ExpandEnv(v)
	}
	policy = saidno.Policy{ReadOnlyTools: cfg.ReadOnlyTools, FailClosed: boolOr(cfg.FailClosed, true)}
	ex := &saidno.Extractor{URL: cfg.ExtractorURL, Model: cfg.ExtractorModel, Headers: headers,
		Client: &http.Client{Timeout: 60 * time.Second}}
	store = saidno.NewStore(ex, 2*time.Hour)
	if cfg.DecisionLog != "" {
		f, err := os.OpenFile(cfg.DecisionLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("said-no: decision log: %w", err)
		}
		logFile = f
	}
	fmt.Printf("[said-no] ready: extractor=%s read_only=%v fail_closed=%v\n", cfg.ExtractorModel, cfg.ReadOnlyTools, policy.FailClosed)
	return nil
}

// GetName returns the plugin's name.
func GetName() string { return "said-no" }

// Cleanup closes the decision log.
func Cleanup() error {
	if logFile != nil {
		return logFile.Close()
	}
	return nil
}

// PreLLMHook reads the user's refusals before each model call. Only messages
// with role "user" are read; tool results (web pages included) and assistant
// turns are never passed to the extractor, so they can't add or lift a rule.
func PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	headers := requestHeaders(ctx)
	if headers[internalHeader] != "" || req == nil || req.ChatRequest == nil {
		return req, nil, nil
	}
	users := UserMessages(req.ChatRequest.Input)
	if len(users) == 0 {
		return req, nil, nil
	}
	session := sessionID(headers, users)
	ctx.SetValue(sessionKey, session)
	tools := toolCatalog(req.ChatRequest.Params)
	start := time.Now()
	l := store.Update(context.Background(), session, users, tools)
	writeLog(map[string]any{
		"event": "ledger", "session": session, "rules": l.Rules, "rejected": l.Rejected,
		"read_failed": l.ReadFailed, "fail_reason": l.FailReason, "user_messages": len(users),
		"tools_known": len(tools), "ms": time.Since(start).Milliseconds(),
	})
	return req, nil, nil
}

// PreMCPHook rules on each tool call before it runs. The decision is made by
// code from the validated rules; a blocked call never reaches the tool.
func PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if req == nil || req.ChatAssistantMessageToolCall == nil {
		return req, nil, nil
	}
	start := time.Now()
	call := saidno.Call{Tool: req.GetToolName(), Client: req.ClientName, Arguments: req.ChatAssistantMessageToolCall.Function.Arguments}
	session := sessionFromCtx(ctx)
	l := store.Get(session)
	var d saidno.Decision
	switch {
	case policy.IsReadOnly(call):
		d = saidno.Decision{Allowed: true, ReadOnly: true, Reason: "read-only tool"}
	case l == nil && boolOr(cfg.BlockUnknownConversations, true):
		d = saidno.Decision{Allowed: false, Reason: "this call can't be tied to a conversation, so the user's refusals can't be checked (send the " + cfg.SessionHeader + " header)"}
	case l == nil:
		d = saidno.Decision{Allowed: true, Reason: "unknown conversation, allowed by config"}
	default:
		d = policy.Decide(l.Rules, call, l.ReadFailed)
	}
	var web []string
	if l != nil {
		web = l.WebSourced(call)
	}
	writeLog(map[string]any{
		"event": "decision", "session": session, "tool": call.Tool, "arguments": call.Arguments,
		"allowed": d.Allowed, "read_only": d.ReadOnly, "reason": d.Reason, "rule": d.Rule,
		"web_sourced_arguments": web, "decision_us": time.Since(start).Microseconds(),
	})
	if d.Allowed {
		return req, nil, nil
	}
	msg := fmt.Sprintf("Blocked by Said No: %s. This action was not run. Do not try it again in another way; tell the user it was blocked and why.", d.Reason)
	return req, shortCircuit(req.ChatAssistantMessageToolCall.ID, msg), nil
}

// PostMCPHook remembers tool catalogs and labels what read-only tools return
// as untrusted, so the log can show which later arguments came from the web.
func PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if resp == nil {
		return resp, bifrostErr, nil
	}
	if resp.BifrostMCPListToolsResponse != nil {
		catalogMu.Lock()
		for name, t := range resp.Tools {
			catalog[name] = toTool(name, t)
		}
		catalogMu.Unlock()
	}
	ef := resp.ExtraFields
	if ef.ToolName != "" && resp.ChatMessage != nil && policy.IsReadOnly(saidno.Call{Tool: ef.ToolName, Client: ef.ClientName}) {
		store.AddUntrusted(sessionFromCtx(ctx), messageText(resp.ChatMessage))
	}
	return resp, bifrostErr, nil
}

// UserMessages returns the text of every message with role "user", in order.
func UserMessages(msgs []schemas.ChatMessage) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != schemas.ChatMessageRoleUser {
			continue
		}
		if t := strings.TrimSpace(messageText(&m)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func messageText(m *schemas.ChatMessage) string {
	if m == nil || m.Content == nil {
		return ""
	}
	if m.Content.ContentStr != nil {
		return *m.Content.ContentStr
	}
	var parts []string
	for _, b := range m.Content.ContentBlocks {
		if b.Text != nil {
			parts = append(parts, *b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// toolCatalog merges the tools on the request with the MCP tools seen in list_tools.
func toolCatalog(params *schemas.ChatParameters) []saidno.Tool {
	seen := map[string]saidno.Tool{}
	if params != nil {
		for _, t := range params.Tools {
			if t.Function != nil {
				seen[t.Function.Name] = toTool(t.Function.Name, t)
			}
		}
	}
	catalogMu.Lock()
	for k, v := range catalog {
		if _, ok := seen[k]; !ok {
			seen[k] = v
		}
	}
	catalogMu.Unlock()
	out := make([]saidno.Tool, 0, len(seen))
	for _, t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// toTool describes a tool for the extractor: its description and parameter names.
func toTool(name string, t schemas.ChatTool) saidno.Tool {
	desc := ""
	if t.Function != nil {
		if t.Function.Description != nil {
			desc = *t.Function.Description
		}
		if t.Function.Parameters != nil {
			if b, err := json.Marshal(t.Function.Parameters); err == nil {
				desc += " Parameters: " + string(b)
			}
		}
	}
	if len(desc) > 600 {
		desc = desc[:600] + "…"
	}
	return saidno.Tool{Name: name, Description: desc}
}

func requestHeaders(ctx *schemas.BifrostContext) map[string]string {
	if h, ok := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string); ok {
		return h
	}
	return map[string]string{}
}

// sessionID names the conversation: the session header if sent, otherwise a
// hash of the first user message (stable across one conversation's turns).
func sessionID(headers map[string]string, users []string) string {
	if s := headers[strings.ToLower(cfg.SessionHeader)]; s != "" {
		return s
	}
	sum := sha256.Sum256([]byte(users[0]))
	return "auto-" + hex.EncodeToString(sum[:8])
}

func sessionFromCtx(ctx *schemas.BifrostContext) string {
	if s, ok := ctx.Value(sessionKey).(string); ok && s != "" {
		return s
	}
	return requestHeaders(ctx)[strings.ToLower(cfg.SessionHeader)]
}

// shortCircuit builds the tool result the agent sees instead of running the tool.
func shortCircuit(callID *string, msg string) *schemas.MCPPluginShortCircuit {
	return &schemas.MCPPluginShortCircuit{Response: &schemas.BifrostMCPResponse{
		ChatMessage: &schemas.ChatMessage{
			Role:            schemas.ChatMessageRoleTool,
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: callID},
			Content:         &schemas.ChatMessageContent{ContentStr: &msg},
		},
		ResponsesMessage: &schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: callID,
				Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &msg},
			},
		},
	}}
}

func writeLog(entry map[string]any) {
	entry["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	fmt.Printf("[said-no] %s\n", b)
	if logFile == nil {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	_, _ = logFile.Write(append(b, '\n'))
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
