package ircwire

import (
	"os/exec"
	"strings"
	"testing"
)

// lierc-api imports this package without lierc's irc-go replace, so it
// must not need the forked package (ircevent) or the rest of lierc.
func TestNoIRCClientDeps(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/lierc/lierc/pkg/ircwire").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "github.com/ergochat/irc-go/ircevent" ||
			strings.HasPrefix(dep, "github.com/lierc/lierc/") && dep != "github.com/lierc/lierc/pkg/ircwire" {
			t.Errorf("pkg/ircwire depends on %s", dep)
		}
	}
}
