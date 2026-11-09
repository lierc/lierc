package main

import (
	"errors"
	"testing"
)

// Each reply counts one query under its result.
func TestReplyCountsResults(t *testing.T) {
	found := func(string, string) (string, error) { return "relaychat", nil }
	none := func(string, string) (string, error) { return "", errNoUser }
	blank := func(string, string) (string, error) { return "  ", nil } // cleans to nothing
	broken := func(string, string) (string, error) { return "", errors.New("portmap down") }

	for _, tt := range []struct {
		result string
		line   string
		find   func(string, string) (string, error)
	}{
		{"user", "40000, 6667", found},
		{"no_user", "40000, 6667", none},
		{"no_user", "40000, 6667", blank},
		{"error", "40000, 6667", broken},
		{"invalid", "not a query", found},
		{"invalid", "0, 6667", found},
	} {
		before := map[string]uint64{}
		for r, c := range queries {
			before[r] = c.Get()
		}
		reply(tt.line, tt.find)
		for r, c := range queries {
			want := before[r]
			if r == tt.result {
				want++
			}
			if got := c.Get(); got != want {
				t.Errorf("%q: %s = %d, want %d", tt.line, r, got, want)
			}
		}
	}
}
