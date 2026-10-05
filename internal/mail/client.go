// Package mail wraps the IMAP connection to Proton Mail Bridge. Tools talk to
// it through plain Go types and never see IMAP details.
package mail

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type Options struct {
	Addr     string
	User     string
	Password string
	TLS      *tls.Config
}

// Client holds a single IMAP connection, dialled on first use and redialled
// after it drops. Calls are serialised because IMAP state (the selected
// mailbox) is per connection.
type Client struct {
	opts Options

	mu   sync.Mutex
	conn *imapclient.Client
}

func New(opts Options) *Client {
	return &Client{opts: opts}
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Logout().Wait()
	c.conn.Close()
	c.conn = nil
	return err
}

// do runs fn with a live, authenticated connection. If the connection has
// dropped it is redialled once before fn runs. fn itself is never retried, so
// a write that fails mid-flight is not repeated blindly.
func (c *Client) do(fn func(*imapclient.Client) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil && isClosed(c.conn) {
		c.conn.Close()
		c.conn = nil
	}
	if c.conn == nil {
		conn, err := c.dial()
		if err != nil {
			return err
		}
		c.conn = conn
	}
	err := fn(c.conn)
	if err != nil && isClosed(c.conn) {
		c.conn.Close()
		c.conn = nil
	}
	return err
}

func (c *Client) dial() (*imapclient.Client, error) {
	conn, err := imapclient.DialStartTLS(c.opts.Addr, &imapclient.Options{TLSConfig: c.opts.TLS})
	if err != nil {
		return nil, fmt.Errorf("connect to Bridge at %s: %w", c.opts.Addr, err)
	}
	if err := conn.Login(c.opts.User, c.opts.Password).Wait(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("login to Bridge: %w", err)
	}
	return conn, nil
}

func isClosed(conn *imapclient.Client) bool {
	select {
	case <-conn.Closed():
		return true
	default:
		return conn.State() == imap.ConnStateLogout
	}
}

// TLSConfig builds the client TLS config. With certPath set, the server must
// present exactly that certificate (Bridge's cert is self-signed, so this is
// pinning rather than CA validation). With insecureLoopback set and no cert,
// verification is skipped; config validation restricts that to loopback.
func TLSConfig(certPath, host string, insecureLoopback bool) (*tls.Config, error) {
	if certPath == "" {
		if !insecureLoopback {
			return nil, errors.New("no Bridge certificate configured")
		}
		return &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, nil
	}
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read Bridge certificate: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s: no PEM certificate found", certPath)
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, fmt.Errorf("%s: %w", certPath, err)
	}
	pinned := block.Bytes
	return &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		// Standard verification would fail on hostname and issuer for a
		// self-signed cert; the pin check below replaces it entirely.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !bytes.Equal(cs.PeerCertificates[0].Raw, pinned) {
				return errors.New("Bridge certificate does not match the pinned BRIDGE_CERT")
			}
			return nil
		},
	}, nil
}

// HostOf returns the host part of addr, or addr itself if it has no port.
func HostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
