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
	SASL      bool
	Alias     string
	Channels  []string
	Highlight []string
}

func (c *IRCConfig) Display() string {
	if c.Alias != "" {
		return c.Alias
	} else {
		return c.Host
	}
}

func (c *IRCConfig) Server() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}
