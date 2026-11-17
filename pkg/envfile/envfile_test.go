package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(vars map[string]string) ([]string, func(string) string) {
	var environ []string
	for k, v := range vars {
		environ = append(environ, k+"="+v)
	}
	return environ, func(k string) string { return vars[k] }
}

func writeSecret(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWithFilesReadsFile(t *testing.T) {
	// one trailing newline is dropped; inner ones and spaces are kept
	p := writeSecret(t, "s3cret pass\n")
	getenv, err := WithFiles(envOf(map[string]string{"POSTGRES_PASSWORD_FILE": p, "POSTGRES_USER": "lierc"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := getenv("POSTGRES_PASSWORD"); got != "s3cret pass" {
		t.Errorf("POSTGRES_PASSWORD = %q", got)
	}
	if got := getenv("POSTGRES_USER"); got != "lierc" {
		t.Errorf("plain setting = %q", got)
	}
}

func TestWithFilesKeepsBareFileSettings(t *testing.T) {
	// with no _FILE the plain variable is used, as before
	getenv, err := WithFiles(envOf(map[string]string{"POSTGRES_PASSWORD": "p"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := getenv("POSTGRES_PASSWORD"); got != "p" {
		t.Errorf("POSTGRES_PASSWORD = %q", got)
	}
}

func TestWithFilesMissingFile(t *testing.T) {
	_, err := WithFiles(envOf(map[string]string{"VAPID_PRIVATE_FILE": "/nonexistent/x"}))
	if err == nil || !strings.Contains(err.Error(), "VAPID_PRIVATE_FILE") {
		t.Errorf("err = %v, want one naming VAPID_PRIVATE_FILE", err)
	}
}

func TestWithFilesBothSet(t *testing.T) {
	p := writeSecret(t, "a")
	_, err := WithFiles(envOf(map[string]string{"SMTP_OPTS_FILE": p, "SMTP_OPTS": "b"}))
	if err == nil || !strings.Contains(err.Error(), "SMTP_OPTS") {
		t.Errorf("err = %v, want one saying both are set", err)
	}
}
