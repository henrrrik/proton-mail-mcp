package render

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // register non-UTF-8 charsets
	"github.com/emersion/go-message/mail"
)

// maxPartBytes bounds how much of any one MIME part is read into memory.
const maxPartBytes = 25 << 20

type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

func (a Address) String() string {
	if a.Name == "" {
		return a.Address
	}
	return (&mail.Address{Name: a.Name, Address: a.Address}).String()
}

type Attachment struct {
	Part        string `json:"part"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	Inline      bool   `json:"inline,omitempty"`
}

// Message is a parsed email. Every string field is email-derived and must be
// wrapped before it reaches the model.
type Message struct {
	From       []Address
	To         []Address
	Cc         []Address
	Bcc        []Address
	ReplyTo    []Address
	Subject    string
	Date       time.Time
	MessageID  string
	InReplyTo  []string
	References []string

	Text        string // best plain-text rendering of the body
	Attachments []Attachment
}

// Parse reads a raw RFC 5322 message.
func Parse(raw []byte) (*Message, error) {
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return nil, ErrUnparseable
	}
	h := mail.Header{Header: e.Header}
	m := &Message{
		From:    addresses(h, "From"),
		To:      addresses(h, "To"),
		Cc:      addresses(h, "Cc"),
		Bcc:     addresses(h, "Bcc"),
		ReplyTo: addresses(h, "Reply-To"),
	}
	if m.Subject, err = h.Subject(); err != nil {
		m.Subject = h.Get("Subject")
	}
	m.Date, _ = h.Date()
	m.MessageID, _ = h.MessageID()
	m.InReplyTo, _ = h.MsgIDList("In-Reply-To")
	m.References, _ = h.MsgIDList("References")

	var plain, htmlBody string
	walk(e, "", func(path string, p *message.Entity) {
		ct, params, _ := p.Header.ContentType()
		disp, dparams, _ := p.Header.ContentDisposition()
		name := dparams["filename"]
		if name == "" {
			name = params["name"]
		}
		isAttachment := disp == "attachment" || name != "" || !strings.HasPrefix(ct, "text/")
		if !isAttachment && (ct == "text/plain" || ct == "text/html" || ct == "") {
			body, _ := io.ReadAll(io.LimitReader(p.Body, maxPartBytes))
			if ct == "text/html" && htmlBody == "" {
				htmlBody = string(body)
			} else if ct != "text/html" && plain == "" {
				plain = string(body)
			}
			return
		}
		n, _ := io.Copy(io.Discard, io.LimitReader(p.Body, maxPartBytes))
		m.Attachments = append(m.Attachments, Attachment{
			Part: path, Filename: name, ContentType: ct, Size: int(n),
			Inline: disp == "inline" || (disp == "" && p.Header.Get("Content-Id") != ""),
		})
	})
	switch {
	case strings.TrimSpace(plain) != "":
		m.Text = Clean(plain)
	case htmlBody != "":
		m.Text = HTMLToText(htmlBody)
	}
	return m, nil
}

var (
	// ErrNoPart is returned when a part path does not exist or is a container.
	ErrNoPart = errors.New("no such attachment part")
	// ErrUnparseable deliberately carries no detail: parser errors quote the
	// offending input, which is attacker-controlled and must not reach the
	// model outside an untrusted block.
	ErrUnparseable = errors.New("the message could not be parsed")
)

// Part returns the decoded content of the leaf part at path, as numbered in
// Message.Attachments.
func Part(raw []byte, path string) (body []byte, contentType, filename string, err error) {
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return nil, "", "", ErrUnparseable
	}
	err = ErrNoPart
	walk(e, "", func(p string, ent *message.Entity) {
		if p != path {
			return
		}
		ct, params, _ := ent.Header.ContentType()
		_, dparams, _ := ent.Header.ContentDisposition()
		filename = dparams["filename"]
		if filename == "" {
			filename = params["name"]
		}
		contentType = ct
		if body, err = io.ReadAll(io.LimitReader(ent.Body, maxPartBytes)); err != nil {
			err = ErrUnparseable
		}
	})
	return body, contentType, filename, err
}

// walk visits every leaf part with its IMAP-style path ("1", "1.2", ...).
// A non-multipart message has a single part "1".
func walk(e *message.Entity, path string, fn func(string, *message.Entity)) {
	mr := e.MultipartReader()
	if mr == nil {
		if path == "" {
			path = "1"
		}
		fn(path, e)
		return
	}
	for i := 1; ; i++ {
		p, err := mr.NextPart()
		if err != nil {
			return // io.EOF or a malformed part: stop either way
		}
		child := strconv.Itoa(i)
		if path != "" {
			child = path + "." + child
		}
		if ct, _, _ := p.Header.ContentType(); ct == "message/rfc822" {
			// Treat forwarded messages as opaque attachments.
			fn(child, p)
			continue
		}
		walk(p, child, fn)
	}
}

func addresses(h mail.Header, key string) []Address {
	list, err := h.AddressList(key)
	if err != nil {
		// Fall back to the decoded raw header so a malformed list is still visible.
		if v := h.Get(key); v != "" {
			dec := new(mime.WordDecoder)
			if s, err := dec.DecodeHeader(v); err == nil {
				v = s
			}
			return []Address{{Address: v}}
		}
		return nil
	}
	out := make([]Address, len(list))
	for i, a := range list {
		out[i] = Address{Name: a.Name, Address: a.Address}
	}
	return out
}
