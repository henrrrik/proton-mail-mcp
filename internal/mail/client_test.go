package mail_test

import (
	"io"
	"net"
	"strings"
	"sync"
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

// proxy forwards TCP to addr and can sever every live connection, as a
// Bridge restart does.
type proxy struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newProxy(t *testing.T, addr string) *proxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{ln: ln}
	t.Cleanup(func() { ln.Close(); p.kill() })
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			out, err := net.Dial("tcp", addr)
			if err != nil {
				in.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, in, out)
			p.mu.Unlock()
			go func() { io.Copy(out, in); out.Close() }()
			go func() { io.Copy(in, out); in.Close() }()
		}
	}()
	return p
}

func (p *proxy) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}

func TestReconnectAfterBridgeDrop(t *testing.T) {
	srv := mailtest.Start(t)
	srv.Append(t, "INBOX", "Subject: one\n\nhi")
	p := newProxy(t, srv.Addr)
	tlsCfg, err := mail.TLSConfig(srv.CertPath, "127.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	c := mail.New(mail.Options{Addr: p.ln.Addr().String(), User: mailtest.User, Password: mailtest.Password, TLS: tlsCfg})
	defer c.Close()

	if _, err := c.ListMessages("INBOX", 10, "", false); err != nil {
		t.Fatal(err)
	}
	p.kill()
	page, err := c.ListMessages("INBOX", 10, "", false)
	if err != nil {
		t.Fatalf("read after drop: %v", err)
	}
	if len(page.Messages) != 1 {
		t.Errorf("got %d messages", len(page.Messages))
	}
	p.kill()
	id, _ := mail.ParseID(page.Messages[0].ID)
	if _, err := c.SetFlags([]mail.ID{id}, nil, nil); err != nil {
		t.Errorf("a write on a dead idle connection should redial first: %v", err)
	}
}

func TestParseID(t *testing.T) {
	id, err := mail.ParseID("Labels/a:b:7:42")
	if err != nil || id.Folder != "Labels/a:b" || id.UIDValidity != 7 || id.UID != 42 {
		t.Errorf("got %+v %v", id, err)
	}
	if id.String() != "Labels/a:b:7:42" {
		t.Errorf("round trip: %s", id)
	}
	for _, bad := range []string{"", "INBOX", "INBOX:1", ":1:2", "INBOX:x:2", "INBOX:1:0", "INBOX:1:-2"} {
		if _, err := mail.ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) accepted", bad)
		}
	}
}

func TestFold(t *testing.T) {
	for in, want := range map[string]string{"Räkning": "rakning", "Pelcová": "pelcova", "Straße": "strasse", "Łódź": "lodz", "Søren": "soren"} {
		if got := mail.Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}
