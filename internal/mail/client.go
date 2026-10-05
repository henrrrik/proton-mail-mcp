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
	"strings"
	"sync"
	"time"

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
	raw  net.Conn // under conn, for per-operation deadlines

	specialMu sync.Mutex
	special   *Special
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

// read runs fn on a live connection. If the connection drops during fn,
// it redials and runs fn once more: reads are safe to repeat.
func (c *Client) read(fn func(*imapclient.Client) error) error {
	err := c.do(false, fn)
	if errors.Is(err, errConnLost) {
		err = c.do(false, fn)
	}
	return unwrapLost(err)
}

// write runs fn on a connection checked with NOOP just before, and never
// retries it: a write that fails mid-flight may still have been applied.
func (c *Client) write(fn func(*imapclient.Client) error) error {
	err := c.do(true, fn)
	if errors.Is(err, errConnLost) {
		return fmt.Errorf("connection to Bridge dropped during a change; it may or may not have been applied, so check before retrying: %w", unwrapLost(err))
	}
	return err
}

var errConnLost = errors.New("connection lost")

type lostError struct{ err error }

func (e lostError) Error() string        { return e.err.Error() }
func (e lostError) Is(target error) bool { return target == errConnLost }
func (e lostError) Unwrap() error        { return e.err }

func unwrapLost(err error) error {
	if l, ok := err.(lostError); ok {
		return l.err
	}
	return err
}

// do runs fn with a live, authenticated connection, dialling first if there
// is none (or, with probe, if the current one fails a NOOP). If fn fails because the connection died, the connection is
// discarded and the error is marked with errConnLost.
func (c *Client) do(probe bool, fn func(*imapclient.Client) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.raw.SetDeadline(time.Now().Add(opTimeout))
		if isClosed(c.conn) || (probe && c.conn.Noop().Wait() != nil) {
			c.conn.Close()
			c.conn = nil
		}
	}
	if c.conn == nil {
		if err := c.dialWithBackoff(); err != nil {
			return err
		}
	}
	// A hung Bridge must not block every tool forever.
	c.raw.SetDeadline(time.Now().Add(opTimeout))
	defer func() {
		if c.raw != nil {
			c.raw.SetDeadline(time.Time{})
		}
	}()
	err := fn(c.conn)
	var imapErr *imap.Error
	if err != nil && !errors.As(err, &imapErr) && connDied(c.conn) {
		c.conn.Close()
		c.conn = nil
		return lostError{err}
	}
	return err
}

// Bridge restarts drop connections; give it a moment to come back.
var dialBackoff = []time.Duration{0, 250 * time.Millisecond, time.Second, 3 * time.Second}

// opTimeout bounds one tool's IMAP work, including dialling.
const opTimeout = 2 * time.Minute

func (c *Client) dialWithBackoff() error {
	var err error
	for _, wait := range dialBackoff {
		time.Sleep(wait)
		if err = c.dial(); err == nil {
			return nil
		}
		var imapErr *imap.Error
		if errors.As(err, &imapErr) || strings.Contains(err.Error(), "pinned") {
			return err // a refused login or a wrong certificate will not fix itself
		}
	}
	return err
}

func (c *Client) dial() error {
	raw, err := net.DialTimeout("tcp", c.opts.Addr, 30*time.Second)
	if err != nil {
		return fmt.Errorf("connect to Bridge at %s: %w", c.opts.Addr, err)
	}
	raw.SetDeadline(time.Now().Add(opTimeout))
	conn, err := imapclient.NewStartTLS(raw, &imapclient.Options{TLSConfig: c.opts.TLS})
	if err != nil {
		raw.Close()
		return fmt.Errorf("connect to Bridge at %s: %w", c.opts.Addr, err)
	}
	if err := conn.Login(c.opts.User, c.opts.Password).Wait(); err != nil {
		conn.Close()
		return fmt.Errorf("login to Bridge: %w", err)
	}
	c.conn, c.raw = conn, raw
	return nil
}

// connDied waits briefly for the client to notice a dead connection.
func connDied(conn *imapclient.Client) bool {
	select {
	case <-conn.Closed():
		return true
	case <-time.After(100 * time.Millisecond):
		return isClosed(conn)
	}
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
				return errors.New("the Bridge certificate does not match the pinned BRIDGE_CERT")
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
