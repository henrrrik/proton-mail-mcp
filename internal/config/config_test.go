package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Load("", env(map[string]string{
		"PROTON_USER":              "me@proton.me",
		"PROTON_BRIDGE_PASSWORD":   "secret",
		"BRIDGE_INSECURE_LOOPBACK": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.ReadOnly {
		t.Error("ReadOnly should default to true")
	}
	if c.IMAPAddr != DefaultIMAPAddr {
		t.Errorf("IMAPAddr = %q", c.IMAPAddr)
	}
}

func TestFileThenEnvOverride(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "c.toml")
	body := `user = "file@proton.me"
password_file = "` + pw + `"
bridge_cert = "/tmp/cert.pem"
read_only = false
deny_folders = ["Spam"]
`
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(cfg, env(map[string]string{
		"PROTON_USER":   "env@proton.me",
		"ALLOW_FOLDERS": "INBOX, Labels/Work ,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.User != "env@proton.me" {
		t.Errorf("User = %q, env should win", c.User)
	}
	if c.Password != "from-file" {
		t.Errorf("Password = %q", c.Password)
	}
	if c.ReadOnly {
		t.Error("ReadOnly should come from file")
	}
	if strings.Join(c.AllowFolders, "|") != "INBOX|Labels/Work" {
		t.Errorf("AllowFolders = %q", c.AllowFolders)
	}
	if strings.Join(c.DenyFolders, "|") != "Spam" {
		t.Errorf("DenyFolders = %q", c.DenyFolders)
	}
}

func TestValidation(t *testing.T) {
	base := map[string]string{
		"PROTON_USER":            "me@proton.me",
		"PROTON_BRIDGE_PASSWORD": "secret",
		"BRIDGE_CERT":            "/tmp/cert.pem",
	}
	tests := []struct {
		name    string
		extra   map[string]string
		wantErr string
	}{
		{"remote refused", map[string]string{"IMAP_ADDR": "10.0.0.5:1143"}, "not loopback"},
		{"remote allowed", map[string]string{"IMAP_ADDR": "10.0.0.5:1143", "ALLOW_REMOTE_IMAP": "true"}, ""},
		{"insecure needs loopback", map[string]string{"IMAP_ADDR": "10.0.0.5:1143", "ALLOW_REMOTE_IMAP": "true", "BRIDGE_INSECURE_LOOPBACK": "1"}, "only applies to loopback"},
		{"no cert", map[string]string{"BRIDGE_CERT": ""}, "BRIDGE_CERT is required"},
		{"bad bool", map[string]string{"READ_ONLY": "maybe"}, "READ_ONLY"},
		{"missing user", map[string]string{"PROTON_USER": ""}, "PROTON_USER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			for k, v := range tt.extra {
				m[k] = v
			}
			_, err := Load("", env(m))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
