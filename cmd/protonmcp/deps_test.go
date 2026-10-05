package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The server must have no way to send mail. This guards against an SMTP
// package creeping in through a direct or transitive dependency.
func TestNoSMTPDependency(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.Contains(strings.ToLower(pkg), "smtp") {
			t.Errorf("binary depends on %s", pkg)
		}
	}
}
