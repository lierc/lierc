package notify

import (
	"testing"
)

func TestSetSMTPOpts(t *testing.T) {
	c := &NotifierConfig{}

	if err := c.SetSMTPOpts(""); err != nil {
		t.Fatalf("empty opts: %s", err)
	}

	if host, port := c.SMTPAddress(); host != "127.0.0.1" || port != 25 {
		t.Errorf("expected default 127.0.0.1:25, got %s:%d", host, port)
	}

	if err := c.SetSMTPOpts("Host,smtp.example.com,Port,465,SSL,1,AuthUser,me,AuthPass,secret"); err != nil {
		t.Fatalf("full opts: %s", err)
	}

	if host, port := c.SMTPAddress(); host != "smtp.example.com" || port != 465 {
		t.Errorf("expected smtp.example.com:465, got %s:%d", host, port)
	}

	if !c.SMTPSSL {
		t.Errorf("expected SSL to be enabled")
	}

	if c.SMTPAuthUser != "me" || c.SMTPAuthPass != "secret" {
		t.Errorf("expected me/secret, got %s/%s", c.SMTPAuthUser, c.SMTPAuthPass)
	}

	if c.SMTPAuth("smtp.example.com") == nil {
		t.Errorf("expected auth to be set")
	}

	if err := c.SetSMTPOpts("Host,smtp.example.com,Port"); err == nil {
		t.Errorf("expected error on odd number of values")
	}

	if err := c.SetSMTPOpts("Host,smtp.example.com,Bogus,1"); err == nil {
		t.Errorf("expected error on unknown opt")
	}

	if err := c.SetSMTPOpts("Port,nope"); err == nil {
		t.Errorf("expected error on invalid port")
	}

	c = &NotifierConfig{}

	if err := c.SetSMTPOpts("Host,smtp.example.com"); err != nil {
		t.Fatalf("host only: %s", err)
	}

	if host, port := c.SMTPAddress(); host != "smtp.example.com" || port != 25 {
		t.Errorf("expected smtp.example.com:25, got %s:%d", host, port)
	}

	if c.SMTPAuth("smtp.example.com") != nil {
		t.Errorf("expected no auth without AuthUser")
	}
}
