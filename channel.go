package lierc

type IRCTopic struct {
	Topic string
	User  string
	Time  int64
}

type IRCChannel struct {
	Topic *IRCTopic
	Nicks map[string]bool
	Name  string
}
