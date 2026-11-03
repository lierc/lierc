package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPortmapURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1":      "http://127.0.0.1:5005/portmap",
		"127.0.0.1:5008": "http://127.0.0.1:5008/portmap",
		"liercd":         "http://liercd:5005/portmap",
		"liercd-next:80": "http://liercd-next:80/portmap",
	}
	for host, want := range cases {
		if got := portmapURL(host); got != want {
			t.Errorf("portmapURL(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestPortmapURLs(t *testing.T) {
	if got, want := portmapURLs("a", ""), []string{"http://a:5005/portmap"}; !reflect.DeepEqual(got, want) {
		t.Errorf("without next: %v, want %v", got, want)
	}
	got := portmapURLs("a", "127.0.0.1:5008")
	want := []string{"http://a:5005/portmap", "http://127.0.0.1:5008/portmap"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with next: %v, want %v", got, want)
	}
}

func serve(t *testing.T, body string, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/portmap" {
			t.Errorf("path %q", r.URL.Path)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/portmap"
}

func deadURL(t *testing.T) string {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/portmap"
	srv.Close()
	return url
}

func TestLookup(t *testing.T) {
	mainURL := serve(t, `[["alice","40000","6667"]]`, http.StatusOK)
	next := serve(t, `[["bob","40001","6697"]]`, http.StatusOK)
	urls := []string{mainURL, next}

	if u, err := lookup(urls, "40000", "6667"); err != nil || u != "alice" {
		t.Errorf("main: %q, %v", u, err)
	}
	if u, err := lookup(urls, "40001", "6697"); err != nil || u != "bob" {
		t.Errorf("next: %q, %v", u, err)
	}
	if _, err := lookup(urls, "40002", "6667"); !errors.Is(err, errNoUser) {
		t.Errorf("unknown connection: %v, want errNoUser", err)
	}
	// Ports must match as a pair.
	if _, err := lookup(urls, "40000", "6697"); !errors.Is(err, errNoUser) {
		t.Errorf("mismatched pair: %v, want errNoUser", err)
	}
}

func TestLookupWithAnInstanceDown(t *testing.T) {
	next := serve(t, `[["bob","40001","6697"]]`, http.StatusOK)
	urls := []string{deadURL(t), next}

	// Found on the instance that answered.
	if u, err := lookup(urls, "40001", "6697"); err != nil || u != "bob" {
		t.Errorf("found on next: %q, %v", u, err)
	}
	// Not found anywhere readable: it may be main's, so not NO-USER.
	if _, err := lookup(urls, "40000", "6667"); err == nil || errors.Is(err, errNoUser) {
		t.Errorf("missing with main down: %v, want a non-NO-USER error", err)
	}
}

func TestLookupBadResponses(t *testing.T) {
	for name, url := range map[string]string{
		"status": serve(t, `[]`, http.StatusInternalServerError),
		"json":   serve(t, `not json`, http.StatusOK),
	} {
		if _, err := lookup([]string{url}, "1", "2"); err == nil || errors.Is(err, errNoUser) {
			t.Errorf("%s: %v, want a non-NO-USER error", name, err)
		}
	}
}

func TestReply(t *testing.T) {
	find := func(user string, err error) func(string, string) (string, error) {
		return func(local, remote string) (string, error) {
			if local != "40000" || remote != "6667" {
				t.Errorf("find(%q, %q)", local, remote)
			}
			return user, err
		}
	}
	cases := []struct {
		name string
		user string
		err  error
		want string
	}{
		{"found", "alice", nil, "40000, 6667 : USERID : UNIX : alice\n"},
		{"no user", "", errNoUser, "40000, 6667 : ERROR : NO-USER\n"},
		{"failure", "", errors.New("down"), "40000, 6667 : ERROR : UNKNOWN-ERROR\n"},
	}
	for _, c := range cases {
		if got := reply("40000 , 6667", find(c.user, c.err)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	if got, want := reply("garbage", nil), "garbage : ERROR : INVALID-PORT\n"; got != want {
		t.Errorf("invalid: %q, want %q", got, want)
	}
}
