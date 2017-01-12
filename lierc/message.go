package lierc

import (
	"strings"
	"time"
)

type IRCPrefix struct {
	Name   string
	User   string
	Server string
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

	message := IRCMessage{
		Raw:    raw,
		Prefix: &IRCPrefix{},
		Time:   now,
	}

	if line[0] == ':' {
		x := strings.SplitN(line[1:], " ", 2)
		y := strings.SplitN(x[0], "!", 2)
		message.Prefix.Name = y[0]

		if len(y) == 2 {
			z := strings.SplitN(y[1], "@", 2)
			message.Prefix.User = z[0]
			message.Prefix.Server = z[1]
		}

		line = x[1]
	}

	if strings.Index(line, " :") != -1 {
		x := strings.SplitN(line, " :", 2)
		params := strings.Split(strings.TrimSpace(x[0]), " ")
		params = append(params, x[1])
		message.Command = params[0]
		message.Params = params[1:]
	} else {
		x := strings.Split(strings.TrimSpace(line), " ")
		message.Command = x[0]
		message.Params = x[1:]
	}

	return &message
}
