package lierc

import (
	"testing"
)

func TestParseIRCMessage(t *testing.T) {
	tests := []map[string]string{
		{
			"line":   ":AHHHH!ron!aaron@blolol.irc fart",
			"name":   "AHHHH!ron",
			"server": "blolol.irc",
			"user":   "aaron",
		},
		{
			"line":   ":lee!leedo@blolol.irc fart lol",
			"name":   "lee",
			"server": "blolol.irc",
			"user":   "leedo",
		},
		{
			"line":   "@msgid=yzvb3a8gvqwab7nerk8v4r8wji :leedo!~lee@d4e8yxp8tyq56.aesthetes.chat JOIN #fart",
			"name":   "leedo",
			"server": "d4e8yxp8tyq56.aesthetes.chat",
			"user":   "~lee",
			"msgid":  "yzvb3a8gvqwab7nerk8v4r8wji",
		},
	}

	for _, test := range tests {
		err, msg := ParseIRCMessage(test["line"])

		t.Log(test["line"])

		if err != nil {
			t.Fatal(err)
		}

		if msg.Prefix.Name != test["name"] {
			t.Errorf("name %q != %q", msg.Prefix.Name, test["name"])
		}
		if msg.Prefix.Server != test["server"] {
			t.Errorf("name %q != %q", msg.Prefix.Server, test["server"])
		}
		if msg.Prefix.User != test["user"] {
			t.Errorf("name %q != %q", msg.Prefix.User, test["user"])
		}
		if test["msgid"] != "" && msg.Tags["msgid"] != test["msgid"] {
			t.Errorf("message id %q != %q", msg.Tags["msgid"], test["msgid"])
		}
	}
}
