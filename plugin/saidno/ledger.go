package saidno

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// RuleSource is anything that can extract rules from a message: the real
// Extractor, or a fake in tests.
type RuleSource interface {
	Extract(ctx context.Context, current string, earlier []string, tools []Tool) ([]Rule, error)
}

// Ledger is the state for one conversation: the rules read from its user
// messages, whether reading failed, and the untrusted text read-only tools
// brought in (kept only to label web-sourced arguments in the log).
type Ledger struct {
	Rules      []Rule
	Rejected   []Rejected
	ReadFailed bool
	FailReason string
	UserText   []string
	Untrusted  []string
	updated    time.Time
}

// Store keeps ledgers per conversation and caches extraction results, so an
// unchanged user message is never sent to the model twice.
type Store struct {
	mu      sync.Mutex
	ledgers map[string]*Ledger
	cache   map[string][]Rule
	ttl     time.Duration
	Source  RuleSource
	// ExtractCalls counts model calls, for the cost line in the measurements.
	ExtractCalls int
}

// NewStore returns a Store that forgets conversations idle for longer than ttl.
func NewStore(src RuleSource, ttl time.Duration) *Store {
	return &Store{ledgers: map[string]*Ledger{}, cache: map[string][]Rule{}, ttl: ttl, Source: src}
}

// cacheKey identifies one extraction: the earlier messages, the current one and the catalog.
func cacheKey(current string, earlier []string, tools []Tool) string {
	h := sha256.New()
	for _, m := range earlier {
		h.Write([]byte(m))
		h.Write([]byte{0})
	}
	h.Write([]byte{1})
	h.Write([]byte(current))
	h.Write([]byte{1})
	for _, t := range tools {
		h.Write([]byte(t.Name))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Update rebuilds the ledger for session from the full list of user messages.
// Only user messages are ever passed here; tool results and assistant turns
// can neither create nor lift a rule. Messages without a refusal cue are
// skipped, and cached messages reuse their earlier rules.
func (s *Store) Update(ctx context.Context, session string, userMsgs []string, tools []Tool) *Ledger {
	catalog := make([]string, 0, len(tools))
	for _, t := range tools {
		catalog = append(catalog, t.Name)
	}
	l := &Ledger{UserText: append([]string(nil), userMsgs...), updated: time.Now()}
	for i, msg := range userMsgs {
		if !HasCue(msg) {
			continue
		}
		key := cacheKey(msg, userMsgs[:i], tools)
		s.mu.Lock()
		proposed, hit := s.cache[key]
		s.mu.Unlock()
		if !hit {
			var err error
			s.mu.Lock()
			s.ExtractCalls++
			s.mu.Unlock()
			proposed, err = s.Source.Extract(ctx, msg, userMsgs[:i], tools)
			if err != nil {
				l.ReadFailed = true
				l.FailReason = err.Error()
				continue
			}
			s.mu.Lock()
			s.cache[key] = proposed
			s.mu.Unlock()
		}
		kept, dropped := Validate(proposed, i, msg, catalog)
		l.Rules = append(l.Rules, kept...)
		l.Rejected = append(l.Rejected, dropped...)
	}
	s.mu.Lock()
	if old, ok := s.ledgers[session]; ok {
		l.Untrusted = old.Untrusted
	}
	s.ledgers[session] = l
	s.sweepLocked()
	s.mu.Unlock()
	return l
}

// Get returns the ledger for session, or nil if none is known.
func (s *Store) Get(session string) *Ledger {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ledgers[session]
}

// AddUntrusted records text a read-only tool returned, so the log can say
// which arguments of a later call came from the web and not from the user.
func (s *Store) AddUntrusted(session, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.ledgers[session]
	if !ok {
		l = &Ledger{updated: time.Now()}
		s.ledgers[session] = l
	}
	l.Untrusted = append(l.Untrusted, text)
}

// WebSourced lists argument values of c that appear in untrusted tool output
// but in no user message. It labels the log; it never decides a block.
func (l *Ledger) WebSourced(c Call) []string {
	var out []string
	for _, v := range parseArgs(c.Arguments) {
		for _, part := range strings.Split(v, ", ") {
			p := strings.ToLower(strings.TrimSpace(part))
			if len(p) < 4 {
				continue
			}
			if anyContains(l.Untrusted, p) && !anyContains(l.UserText, p) {
				out = append(out, part)
			}
		}
	}
	return out
}

func anyContains(texts []string, lowerNeedle string) bool {
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), lowerNeedle) {
			return true
		}
	}
	return false
}

// sweepLocked drops ledgers idle for longer than the TTL. Callers hold s.mu.
func (s *Store) sweepLocked() {
	if s.ttl <= 0 {
		return
	}
	cutoff := time.Now().Add(-s.ttl)
	for k, l := range s.ledgers {
		if l.updated.Before(cutoff) {
			delete(s.ledgers, k)
		}
	}
}
