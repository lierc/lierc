package lierc

import (
	"errors"
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
	Tags    map[string]string
	Command string
	Raw     string
	Time    float64
	Params  []string
	Direct  bool
}

func ParseIRCMessage(line string) (error, *IRCMessage) {
	if len(line) == 0 {
		return errors.New("Zero length message"), nil
	}

	raw := line
	now := float64(time.Now().UnixNano()) / float64(time.Second)

	m := IRCMessage{
		Raw:    raw,
		Tags:   make(map[string]string),
		Prefix: &IRCPrefix{Self: false},
		Direct: false,
		Time:   now,
	}

	// looks like it starts with tags
	if line[0] == '@' {
		parts := strings.SplitN(line[1:], " ", 2)

		if len(parts) > 1 {
			tags := strings.Split(parts[0], ";")
			for _, tag := range tags {
				kv := strings.SplitN(tag, "=", 2)
				if len(kv) == 2 {
					m.Tags[kv[0]] = kv[1]
				} else {
					m.Tags[tag] = ""
				}
			}

			line = parts[1]
		}
	}

	// parse message prefix
	if line[0] == ':' {
		parts := strings.SplitN(line[1:], " ", 2)
		from := parts[0]
		bangPos := strings.LastIndex(from, "!")

		if bangPos != -1 {
			m.Prefix.Name = from[0:bangPos]
			if len(from) > bangPos {
				rest := strings.SplitN(from[bangPos+1:], "@", 2)
				m.Prefix.User = rest[0]
				if len(rest) == 2 {
					m.Prefix.Server = rest[1]
				}
			}
		} else {
			m.Prefix.Name = from
		}

		line = parts[1]
	}

	if strings.Index(line, " :") != -1 {
		x := strings.SplitN(line, " :", 2)
		params := strings.Split(strings.TrimSpace(x[0]), " ")
		params = append(params, x[1])
		m.Command = params[0]
		m.Params = params[1:]
	} else {
		x := strings.Split(strings.TrimSpace(line), " ")
		if len(x) > 0 {
			m.Command = x[0]
			if len(x) > 1 {
				m.Params = x[1:]
			}
		} else {
			return errors.New("Message has no command"), nil
		}
	}

	return nil, &m
}
