package lierc

import "strings"

// defaultPrefix is used when the server does not advertise PREFIX.
// Each entry is {symbol, mode}.
var defaultPrefix = [][]byte{{'@', 'o'}, {'+', 'v'}, {'%', 'h'}}

// prefixesFrom is the nick prefixes an ISUPPORT map's PREFIX gives, or
// the default when it's missing or malformed.
func prefixesFrom(isupport map[string]string) [][]byte {
	if prefix, ok := parsePrefix(isupport["PREFIX"]); ok {
		return prefix
	}
	return defaultPrefix
}

// parsePrefix parses an ISUPPORT PREFIX value such as "(ov)@+" into
// {symbol, mode} pairs. It returns false if the value is malformed.
func parsePrefix(value string) ([][]byte, bool) {
	if !strings.HasPrefix(value, "(") {
		return nil, false
	}
	modes, symbols, ok := strings.Cut(value[1:], ")")
	if !ok || modes == "" || len(modes) != len(symbols) {
		return nil, false
	}
	prefix := make([][]byte, len(modes))
	for i := range modes {
		prefix[i] = []byte{symbols[i], modes[i]}
	}
	return prefix, true
}

var isupportEscaper = strings.NewReplacer(`\`, `\x5C`, " ", `\x20`, "=", `\x3D`)

// isupportTokens rebuilds ISUPPORT tokens from the decoded map irc-go keeps,
// escaping the characters that would otherwise break the token syntax.
func isupportTokens(m map[string]string) []string {
	tokens := make([]string, 0, len(m))
	for k, v := range m {
		if v == "" {
			tokens = append(tokens, k)
		} else {
			tokens = append(tokens, k+"="+isupportEscaper.Replace(v))
		}
	}
	return tokens
}
