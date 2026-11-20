package ircwire

import (
	"testing"

	"github.com/ergochat/irc-go/ircevent"
)

// The numerics are irc-go's; only this test imports ircevent (see
// TestNoIRCClientDeps).
func TestNumericsMatchIRCGo(t *testing.T) {
	for name, c := range map[string][2]string{
		"RPL_WELCOME":       {RPL_WELCOME, ircevent.RPL_WELCOME},
		"RPL_YOURHOST":      {RPL_YOURHOST, ircevent.RPL_YOURHOST},
		"RPL_CREATED":       {RPL_CREATED, ircevent.RPL_CREATED},
		"RPL_MYINFO":        {RPL_MYINFO, ircevent.RPL_MYINFO},
		"RPL_ISUPPORT":      {RPL_ISUPPORT, ircevent.RPL_ISUPPORT},
		"RPL_LUSERCLIENT":   {RPL_LUSERCLIENT, ircevent.RPL_LUSERCLIENT},
		"RPL_LUSEROP":       {RPL_LUSEROP, ircevent.RPL_LUSEROP},
		"RPL_LUSERUNKNOWN":  {RPL_LUSERUNKNOWN, ircevent.RPL_LUSERUNKNOWN},
		"RPL_LUSERCHANNELS": {RPL_LUSERCHANNELS, ircevent.RPL_LUSERCHANNELS},
		"RPL_LUSERME":       {RPL_LUSERME, ircevent.RPL_LUSERME},
		"RPL_LOCALUSERS":    {RPL_LOCALUSERS, ircevent.RPL_LOCALUSERS},
		"RPL_GLOBALUSERS":   {RPL_GLOBALUSERS, ircevent.RPL_GLOBALUSERS},
		"RPL_MOTD":          {RPL_MOTD, ircevent.RPL_MOTD},
		"RPL_MOTDSTART":     {RPL_MOTDSTART, ircevent.RPL_MOTDSTART},
		"RPL_ENDOFMOTD":     {RPL_ENDOFMOTD, ircevent.RPL_ENDOFMOTD},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %q, irc-go has %q", name, c[0], c[1])
		}
	}
	if RPL_STATSCONN != "250" {
		t.Errorf("RPL_STATSCONN = %q", RPL_STATSCONN)
	}
}
