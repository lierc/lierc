package lierc

import (
	"reflect"
	"sort"
	"testing"
)

// No handler may panic on a message with too few parameters, whatever the
// prefix; with none, it must leave the channels alone.
func TestHandlersShortParams(t *testing.T) {
	var cmds []string
	for cmd := range handlers {
		cmds = append(cmds, cmd)
	}
	sort.Strings(cmds)
	cmds = append(cmds, "PRIVMSG", "NOTICE")

	prefixes := map[string]*IRCPrefix{
		"full":  {Name: "me", User: "u", Server: "h"},
		"empty": {},
	}
	for _, cmd := range cmds {
		for pname, prefix := range prefixes {
			for n := 0; n <= 2; n++ {
				params := []string{"#a", "x"}[:n]
				c := configClient()
				before := map[string]*IRCChannel{}
				for k, v := range c.Channels {
					before[k] = v
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("%s with %s prefix, %d params: panic: %v", cmd, pname, n, r)
						}
					}()
					p := *prefix
					c.Message(&IRCMessage{Command: cmd, Prefix: &p, Params: params})
				}()
				if n == 0 && !reflect.DeepEqual(c.Channels, before) {
					t.Errorf("%s with %s prefix, 0 params changed Channels", cmd, pname)
				}
			}
		}
	}
}

// QUIT and NICK ask for a multi message; Direct and Self are set for both
// PRIVMSG and NOTICE.
func TestMessageEffects(t *testing.T) {
	c := configClient()
	m, _ := ParseIRCMessage(":bob!u@h QUIT :bye")
	if e := c.Message(m); e.multi == nil || e.multi.Id != "c1" {
		t.Errorf("QUIT: multi %v", e.multi)
	}
	m, _ = ParseIRCMessage(":bob!u@h NOTICE me :hi")
	c.Message(m)
	if !m.Direct {
		t.Error("NOTICE to us not Direct")
	}
}
