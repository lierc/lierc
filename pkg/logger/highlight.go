package logger

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/lierc/lierc/pkg/lierc"
)

var nonBlank = regexp.MustCompile(`\S`)

// Highlighters holds each connection's highlight pattern, built from
// the comma separated words in its config.
type Highlighters struct {
	mu       sync.RWMutex
	patterns map[string]*regexp.Regexp
}

// Replace swaps in a new set of patterns, so connections whose
// highlights were removed stop matching.
func (h *Highlighters) Replace(patterns map[string]*regexp.Regexp) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.patterns = patterns
}

func (h *Highlighters) pattern(connection string) *regexp.Regexp {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.patterns[connection]
}

// Match reports whether m is someone else's PRIVMSG that contains one
// of the connection's highlight words.
func (h *Highlighters) Match(connection string, m *lierc.IRCMessage) bool {
	if m.Prefix.Self || m.Command != "PRIVMSG" || len(m.Params) < 2 {
		return false
	}
	pattern := h.pattern(connection)
	return pattern != nil && pattern.MatchString(m.Params[1])
}

// compileHighlights builds a pattern per connection from their
// highlight config (a JSON list of words). Connections without any
// words are left out; ones that can't be parsed are reported in errs.
func compileHighlights(configs map[string]string) (patterns map[string]*regexp.Regexp, errs []error) {
	patterns = make(map[string]*regexp.Regexp)

	for connection, config := range configs {
		var terms []string
		if err := json.Unmarshal([]byte(config), &terms); err != nil {
			errs = append(errs, fmt.Errorf("highlights for %s: %w", connection, err))
			continue
		}

		re, err := compileHighlight(terms)
		if err != nil {
			errs = append(errs, fmt.Errorf("highlights for %s: %w", connection, err))
			continue
		}
		if re != nil {
			patterns[connection] = re
		}
	}

	return patterns, errs
}

// compileHighlight matches any of terms as whole words, ignoring case.
// It returns nil if there are no non-blank terms.
func compileHighlight(terms []string) (*regexp.Regexp, error) {
	var parts []string

	for _, term := range terms {
		if nonBlank.MatchString(term) {
			parts = append(parts, regexp.QuoteMeta(term))
		}
	}

	if len(parts) == 0 {
		return nil, nil
	}

	return regexp.Compile("(?i)\\b(?:" + strings.Join(parts, "|") + ")\\b")
}
