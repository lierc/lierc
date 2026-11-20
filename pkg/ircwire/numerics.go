package ircwire

// The numerics lierc-api's logger files under a connection's status
// panel, with irc-go's names (ircevent's, which this package can't
// import), and 250, which irc-go has no name for.
const (
	// welcome
	RPL_WELCOME  = "001"
	RPL_YOURHOST = "002"
	RPL_CREATED  = "003"
	RPL_MYINFO   = "004"
	RPL_ISUPPORT = "005"

	// user counts
	RPL_STATSCONN     = "250" // the highest connection count
	RPL_LUSERCLIENT   = "251"
	RPL_LUSEROP       = "252"
	RPL_LUSERUNKNOWN  = "253"
	RPL_LUSERCHANNELS = "254"
	RPL_LUSERME       = "255"
	RPL_LOCALUSERS    = "265"
	RPL_GLOBALUSERS   = "266"

	// MOTD
	RPL_MOTD      = "372"
	RPL_MOTDSTART = "375"
	RPL_ENDOFMOTD = "376"
)
