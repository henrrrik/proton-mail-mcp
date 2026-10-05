// Package draft builds RFC 5322 draft messages. It produces bytes for IMAP
// APPEND to the Drafts folder; nothing here can send mail.
package draft

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"

	"github.com/henrrrik/proton-mail-mcp/internal/render"
)

type Draft struct {
	From       string // the account address; set explicitly so aliases don't misfile
	To, Cc     []string
	Bcc        []string // kept on the draft so it shows when reopened
	Subject    string
	Body       string
	InReplyTo  string
	References []string
}

// Build renders d and returns the raw message and its Message-ID.
func Build(d Draft, now time.Time) ([]byte, string, error) {
	var h mail.Header
	h.SetDate(now)
	from, err := mail.ParseAddress(d.From)
	if err != nil {
		return nil, "", fmt.Errorf("from address %q: %w", d.From, err)
	}
	h.SetAddressList("From", []*mail.Address{from})
	for key, list := range map[string][]string{"To": d.To, "Cc": d.Cc, "Bcc": d.Bcc} {
		addrs, err := ParseAddresses(list)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", key, err)
		}
		if len(addrs) > 0 {
			h.SetAddressList(key, addrs)
		}
	}
	h.SetSubject(strings.Join(strings.Fields(d.Subject), " "))
	if d.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{d.InReplyTo})
	}
	if len(d.References) > 0 {
		h.SetMsgIDList("References", d.References)
	}
	if err := h.GenerateMessageIDWithHostname(domainOf(from.Address)); err != nil {
		return nil, "", err
	}
	msgID, _ := h.MessageID()
	h.Set("MIME-Version", "1.0")
	h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	h.Set("Content-Transfer-Encoding", "quoted-printable")

	var buf bytes.Buffer
	w, err := mail.CreateSingleInlineWriter(&buf, h)
	if err != nil {
		return nil, "", err
	}
	body := strings.ReplaceAll(d.Body, "\r\n", "\n")
	if _, err := w.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n"))); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), msgID, nil
}

// ParseAddresses validates recipient strings like "Anna <a@b.se>".
func ParseAddresses(list []string) ([]*mail.Address, error) {
	var out []*mail.Address
	for _, s := range list {
		a, err := mail.ParseAddress(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("invalid address %q", s)
		}
		out = append(out, a)
	}
	return out, nil
}

// FromMessage recovers the editable fields of an existing draft.
func FromMessage(m *render.Message, body string) Draft {
	d := Draft{Subject: m.Subject, Body: body, References: m.References}
	if len(m.From) > 0 {
		d.From = m.From[0].String()
	}
	d.To = strs(m.To)
	d.Cc = strs(m.Cc)
	d.Bcc = strs(m.Bcc)
	if len(m.InReplyTo) > 0 {
		d.InReplyTo = m.InReplyTo[0]
	}
	return d
}

// Reply builds a reply draft to orig, written by self.
func Reply(orig *render.Message, self, body string, replyAll bool) (Draft, error) {
	if orig.MessageID == "" {
		return Draft{}, errors.New("the original message has no Message-ID, so a reply cannot be threaded")
	}
	to, cc := ReplyRecipients(orig, self, replyAll)
	subject := orig.Subject
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), "re:") {
		subject = "Re: " + subject
	}
	refs := append(append([]string{}, orig.References...), orig.MessageID)
	if len(refs) == 1 && len(orig.InReplyTo) > 0 {
		refs = append([]string{orig.InReplyTo[0]}, refs...)
	}
	sender := "someone"
	if len(orig.From) > 0 {
		sender = orig.From[0].String()
	}
	quoted := fmt.Sprintf("%s\n\nOn %s, %s wrote:\n%s\n", strings.TrimRight(body, "\n"),
		orig.Date.Format("Mon, 2 Jan 2006 at 15:04"), sender, render.Quote(orig.Text))
	return Draft{
		From: self, To: strs(to), Cc: strs(cc), Subject: subject, Body: quoted,
		InReplyTo: orig.MessageID, References: refs,
	}, nil
}

// ReplyRecipients picks who a reply goes to. A reply to your own sent mail
// goes to its original recipients, not back to you.
func ReplyRecipients(orig *render.Message, self string, all bool) (to, cc []render.Address) {
	fromSelf := len(orig.From) > 0 && IsSelf(orig.From[0].Address, self)
	switch {
	case fromSelf:
		to = orig.To
	case len(orig.ReplyTo) > 0:
		to = orig.ReplyTo
	default:
		to = orig.From
	}
	if all {
		if !fromSelf {
			to = append(append([]render.Address{}, to...), orig.To...)
		}
		cc = orig.Cc
	}
	seen := map[string]bool{}
	clean := func(in []render.Address) []render.Address {
		var out []render.Address
		for _, a := range in {
			k := strings.ToLower(a.Address)
			if seen[k] || IsSelf(a.Address, self) {
				continue
			}
			seen[k] = true
			out = append(out, a)
		}
		return out
	}
	to, cc = clean(to), clean(cc)
	if len(to) == 0 && len(cc) == 0 && fromSelf {
		to = []render.Address{{Address: self}} // a genuine note to self
	}
	return to, cc
}

// IsSelf compares addresses ignoring case and +tags.
func IsSelf(addr, self string) bool {
	return canonical(addr) == canonical(self)
}

func canonical(addr string) string {
	if a, err := mail.ParseAddress(addr); err == nil {
		addr = a.Address
	}
	addr = strings.ToLower(strings.TrimSpace(addr))
	local, domain, ok := strings.Cut(addr, "@")
	if !ok {
		return addr
	}
	local, _, _ = strings.Cut(local, "+")
	return local + "@" + domain
}

func domainOf(addr string) string {
	if _, d, ok := strings.Cut(addr, "@"); ok {
		return d
	}
	return "localhost"
}

func strs(as []render.Address) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.String()
	}
	return out
}
