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

func (config *IRCConfig) Server() string {
	return config.Host + ":" + strconv.Itoa(config.Port)
}
