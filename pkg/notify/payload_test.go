package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func longHighlight(text string) *LoggedMessage {
	m := highlight("c1", time.Now())
	m.Message.Tags = map[string]string{"time": "2026-11-09T12:13:16.123Z", "msgid": strings.Repeat("x", 26)}
	m.Message.Params[1] = text
	return m
}

func TestPushPayloadFits(t *testing.T) {
	var ms []*LoggedMessage
	for i := 0; i < 50; i++ {
		ms = append(ms, longHighlight(strings.Repeat("é", 1000)))
	}
	b, err := pushPayload("abc", ms)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxPushPayload {
		t.Fatalf("payload %d bytes", len(b))
	}
	var p struct {
		To    string `json:"to"`
		Count int    `json:"count"`
		Data  []struct {
			ConnectionId string
			Message      struct {
				Prefix struct{ Name string }
				Params []string
				Direct bool
				Tags   map[string]string
			}
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.To != "abc" || p.Count != 50 || len(p.Data) == 0 || len(p.Data) >= 50 {
		t.Fatalf("to %q count %d data %d", p.To, p.Count, len(p.Data))
	}
	d := p.Data[0]
	if d.ConnectionId != "c1" || d.Message.Prefix.Name != "bob" || d.Message.Params[0] != "#c" || d.Message.Tags != nil {
		t.Errorf("first message %+v", d)
	}
	if n := utf8.RuneCountInString(d.Message.Params[1]); n != maxPushText+1 || !strings.HasSuffix(d.Message.Params[1], "…") {
		t.Errorf("text is %d runes", n)
	}
}

func TestPushPayloadSmall(t *testing.T) {
	m := highlight("c1", time.Now())
	m.Message.Direct = true
	b, err := pushPayload("abc", []*LoggedMessage{m, highlight("c2", time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"count":2`) || !strings.Contains(string(b), `"Direct":true`) || strings.Count(string(b), "ConnectionId") != 2 {
		t.Errorf("payload %s", b)
	}
	if !strings.Contains(string(b), `"Params":["#c","hi leedo"]`) {
		t.Errorf("text not kept whole: %s", b)
	}
}

// A full payload survives webpush-go's encryption, and the push asks the
// service to keep it for hours at high urgency.
func TestSendWebPushLargeBacklog(t *testing.T) {
	var ttl, urgency string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ttl, urgency = r.Header.Get("TTL"), r.Header.Get("Urgency")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	n := sendNotifier(t, "")
	var ms []*LoggedMessage
	for i := 0; i < 50; i++ {
		ms = append(ms, longHighlight(strings.Repeat("a", 280)))
	}
	res, _, err := n.SendWebPush(ms, subscription(t, srv.URL+"/push/abc"))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusCreated || ttl != "14400" || urgency != "high" {
		t.Errorf("status %d TTL %q Urgency %q", res.StatusCode, ttl, urgency)
	}
}
