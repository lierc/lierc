package ircwire

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures in testdata/compat have the shapes of real messages, with
// made-up names and text. stored-* are message columns from the logs
// table, which keeps every format liercd has ever sent; current-* are
// chats messages as liercd sends them now.

// Every stored shape still decodes into the current types, with the
// fields the logger and the UI read.
func TestCompatStored(t *testing.T) {
	tests := []struct {
		file, command, prefix string
		params                int
		tag                   string
	}{
		{"stored-2021-privmsg.json", "PRIVMSG", "alice", 2, ""},
		{"stored-2025-tagmsg.json", "TAGMSG", "bob", 1, "+draft/react"},
		{"stored-2026-000.json", "000", "irc.example.org", 2, ""},
	}
	for _, tt := range tests {
		var m IRCMessage
		if err := json.Unmarshal(readFixture(t, tt.file), &m); err != nil {
			t.Errorf("%s: %v", tt.file, err)
			continue
		}
		if m.Command != tt.command || m.Prefix == nil || m.Prefix.Name != tt.prefix ||
			len(m.Params) != tt.params || m.Time == 0 {
			t.Errorf("%s: decoded as %+v (prefix %+v)", tt.file, m, m.Prefix)
		}
		if tt.tag != "" && m.Tags[tt.tag] == "" {
			t.Errorf("%s: tag %s missing", tt.file, tt.tag)
		}
	}
}

// The current format encodes exactly as the fixture: a renamed, removed
// or retyped field fails here. liercd and the logger deploy separately,
// so such a change must be readable by the other side's older version
// first. A new field is safe (older readers ignore it); add it to the
// fixtures.
func TestCompatCurrent(t *testing.T) {
	files, _ := filepath.Glob("testdata/compat/current-*.json")
	if len(files) == 0 {
		t.Fatal("no current fixtures")
	}
	for _, file := range files {
		want := bytes.TrimSpace(readFixture(t, filepath.Base(file)))
		var m IRCClientMessage
		if err := json.Unmarshal(want, &m); err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		got, err := json.Marshal(&m)
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: encodes differently\ngot  %s\nwant %s", file, got, want)
		}
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata/compat", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
