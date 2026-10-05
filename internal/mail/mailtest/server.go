// Package mailtest runs an in-memory IMAP server that looks enough like
// Proton Mail Bridge for tests: STARTTLS with a self-signed certificate and
// Bridge's folder layout.
package mailtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const (
	User     = "me@proton.me"
	Password = "bridge-password"
)

// BridgeFolders mirrors the mailboxes a fresh Bridge account exposes, plus one
// user folder and one label.
var BridgeFolders = []string{"Drafts", "Sent", "Starred", "Archive", "Spam", "Trash", "All Mail", "Folders/Receipts", "Labels/Work"}

type Server struct {
	Addr     string
	CertPath string // PEM of the server's self-signed certificate
	User     *imapmemserver.User
}

// Start runs a server for the duration of the test.
func Start(t testing.TB) *Server {
	t.Helper()
	cert, certPEM := selfSigned(t)
	certPath := filepath.Join(t.TempDir(), "bridge.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	user := imapmemserver.NewUser(User, Password)
	for _, name := range append([]string{"INBOX"}, BridgeFolders...) {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem := imapmemserver.New()
	mem.AddUser(user)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {},
			imap.CapUIDPlus: {}, imap.CapMove: {},
		},
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
		Logger:    discardLogger{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	return &Server{Addr: ln.Addr().String(), CertPath: certPath, User: user}
}

// Append adds a raw RFC 5322 message to mailbox. Like Bridge, the internal
// date is the message's Date header.
func (s *Server) Append(t testing.TB, mailbox, raw string, flags ...imap.Flag) {
	t.Helper()
	date := time.Now()
	if m, err := mail.ReadMessage(strings.NewReader(raw)); err == nil {
		if d, err := m.Header.Date(); err == nil {
			date = d
		}
	}
	raw = strings.ReplaceAll(raw, "\n", "\r\n")
	r := literal{strings.NewReader(raw), int64(len(raw))}
	if _, err := s.User.Append(mailbox, r, &imap.AppendOptions{Flags: flags, Time: date}); err != nil {
		t.Fatal(err)
	}
}

type literal struct {
	*strings.Reader
	n int64
}

func (l literal) Size() int64 { return l.n }

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

func selfSigned(t testing.TB) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// Each calls fn with the raw bytes of every message in mailbox, oldest first.
func (s *Server) Each(t testing.TB, mailbox string, fn func(raw []byte)) {
	t.Helper()
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	c, err := imapclient.DialStartTLS(s.Addr, &imapclient.Options{TLSConfig: tlsCfg})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login(User, Password).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	section := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(imap.SeqSet{imap.SeqRange{Start: 1, Stop: 0}}, &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		fn(m.FindBodySection(section))
	}
}
