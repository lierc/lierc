package lierc

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestParsePrefix(t *testing.T) {
	good := []struct {
		in   string
		want [][]byte
	}{
		{"(ov)@+", [][]byte{{'@', 'o'}, {'+', 'v'}}},
		{"(qaohv)~&@%+", [][]byte{{'~', 'q'}, {'&', 'a'}, {'@', 'o'}, {'%', 'h'}, {'+', 'v'}}},
	}
	for _, c := range good {
		got, ok := parsePrefix(c.in)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("parsePrefix(%q) = %v, %v; want %v, true", c.in, got, ok, c.want)
		}
	}
	for _, in := range []string{"", "(ov)@", "ov@+", "(", "()", "(ov"} {
		if got, ok := parsePrefix(in); ok {
			t.Errorf("parsePrefix(%q) = %v, true; want false", in, got)
		}
	}
}

// unescapeLikeIrcGo applies irc-go's unescapeISupportValue rules: \xHH is
// decoded only for backslash, space, equals and comma.
func unescapeLikeIrcGo(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			switch strings.ToUpper(s[i+2 : i+4]) {
			case "5C":
				b.WriteByte('\\')
			case "20":
				b.WriteByte(' ')
			case "3D":
				b.WriteByte('=')
			case "2C":
				b.WriteByte(',')
			default:
				b.WriteByte(s[i])
				continue
			}
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func sorted(s []string) []string {
	c := append([]string{}, s...)
	sort.Strings(c)
	return c
}

func TestISupportRoundTrip(t *testing.T) {
	m := map[string]string{}
	for _, tok := range strings.Fields(`NETWORK=Foo\x20Bar EXCEPTS CHANLIMIT=#:120,&:50 PREFIX=(ov)@+`) {
		k, v, _ := strings.Cut(tok, "=")
		m[k] = unescapeLikeIrcGo(v)
	}
	got := sorted(isupportTokens(m))
	want := sorted([]string{`NETWORK=Foo\x20Bar`, "EXCEPTS", "CHANLIMIT=#:120,&:50", "PREFIX=(ov)@+"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	got = isupportTokens(map[string]string{"X": `a\b=c`})
	if !reflect.DeepEqual(got, []string{`X=a\x5Cb\x3Dc`}) {
		t.Errorf("got %q", got)
	}

	if got := isupportTokens(nil); got == nil || len(got) != 0 {
		t.Errorf("nil map: got %#v, want empty non-nil", got)
	}
}
