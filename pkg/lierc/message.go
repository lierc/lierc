package lierc

import (
	"strings"
	"time"
)

type IRCPrefix struct {
	Name   string
	User   string
	Server string
	Self   bool
}

type IRCMessage struct {
	Prefix  *IRCPrefix
	Command string
	Raw     string
	Time    float64
	Params  []string
}

func ParseIRCMessage(line string) *IRCMessage {
	raw := line
	now := float64(time.Now().UnixNano()) / float64(time.Second)

	m := IRCMessage{
		Raw:    raw,
		Prefix: &IRCPrefix{Self: false},
		Time:   now,
	}

	if line[0] == ':' {
		x := strings.SplitN(line[1:], " ", 2)
		y := strings.SplitN(x[0], "!", 2)
		m.Prefix.Name = y[0]

		if len(y) == 2 {
			z := strings.SplitN(y[1], "@", 2)
			m.Prefix.User = z[0]
			m.Prefix.Server = z[1]
		}

		line = x[1]
	}

	if strings.Index(line, " :") != -1 {
		x := strings.SplitN(line, " :", 2)
		params := strings.Split(strings.TrimSpace(x[0]), " ")
		params = append(params, x[1])
		m.Command = params[0]
		m.Params = params[1:]
	} else {
		x := strings.Split(strings.TrimSpace(line), " ")
		m.Command = x[0]
		m.Params = x[1:]
	}

	return &m
}
