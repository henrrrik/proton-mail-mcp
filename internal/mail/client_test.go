package mail_test

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/henrrrik/proton-mail-mcp/internal/mail"
	"github.com/henrrrik/proton-mail-mcp/internal/mail/mailtest"
)

func connect(t *testing.T, srv *mailtest.Server, certPath string) *mail.Client {
	t.Helper()
	tlsCfg, err := mail.TLSConfig(certPath, "127.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	c := mail.New(mail.Options{Addr: srv.Addr, User: mailtest.User, Password: mailtest.Password, TLS: tlsCfg})
	t.Cleanup(func() { c.Close() })
	return c
}

func TestListFolders(t *testing.T) {
	srv := mailtest.Start(t)
	srv.Append(t, "INBOX", "Subject: one\n\nhi")
	srv.Append(t, "INBOX", "Subject: two\n\nhi", imap.FlagSeen)
	srv.Append(t, "Labels/Work", "Subject: three\n\nhi")

	c := connect(t, srv, srv.CertPath)
	folders, err := c.ListFolders(func(name string) bool { return name != "Spam" })
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]mail.Folder{}
	for _, f := range folders {
		got[f.Name] = f
	}
	if _, ok := got["Spam"]; ok {
		t.Error("Spam should be filtered out")
	}
	if f := got["INBOX"]; f.Total != 2 || f.Unread != 1 || f.Kind != mail.KindSystem {
		t.Errorf("INBOX = %+v", f)
	}
	if f := got["Labels/Work"]; f.Kind != mail.KindLabel || f.Total != 1 {
		t.Errorf("Labels/Work = %+v", f)
	}
	if f := got["Folders/Receipts"]; f.Kind != mail.KindFolder {
		t.Errorf("Folders/Receipts = %+v", f)
	}
}

func TestWrongCertificateIsRejected(t *testing.T) {
	srv := mailtest.Start(t)
	other := mailtest.Start(t) // different self-signed cert

	c := connect(t, srv, other.CertPath)
	_, err := c.ListFolders(func(string) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("err = %v, want pin mismatch", err)
	}
}
