package lierc

import (
	"strconv"
)

type IRCConfig struct {
	Host      string
	Port      int
	Ssl       bool
	Nick      string
	User      string
	Pass      string
	Channels  []string
	Highlight []string
}

func (c *IRCConfig) Server() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}
