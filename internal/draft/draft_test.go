package draft

import (
	"strings"
	"testing"
	"time"

	"github.com/henrrrik/proton-mail-mcp/internal/render"
)

const self = "me@proton.me"

func addrs(ss ...string) []render.Address {
	out := make([]render.Address, len(ss))
	for i, s := range ss {
		out[i] = render.Address{Address: s}
	}
	return out
}

func names(as []render.Address) string {
	var out []string
	for _, a := range as {
		out = append(out, a.Address)
	}
	return strings.Join(out, ",")
}

func TestReplyRecipients(t *testing.T) {
	tests := []struct {
		name           string
		msg            render.Message
		all            bool
		wantTo, wantCc string
	}{
		{"sender", render.Message{From: addrs("a@x"), To: addrs(self, "b@x"), Cc: addrs("c@x")}, false, "a@x", ""},
		{"reply-to wins", render.Message{From: addrs("a@x"), ReplyTo: addrs("list@x"), To: addrs(self)}, false, "list@x", ""},
		{"reply all drops self", render.Message{From: addrs("a@x"), To: addrs(self, "b@x"), Cc: addrs("c@x", "Me+news@Proton.me")}, true, "a@x,b@x", "c@x"},
		{"own sent mail", render.Message{From: addrs("me+work@proton.me"), ReplyTo: addrs(self), To: addrs("a@x"), Cc: addrs("c@x")}, false, "a@x", ""},
		{"own sent mail, all", render.Message{From: addrs(self), To: addrs("a@x"), Cc: addrs("c@x")}, true, "a@x", "c@x"},
		{"note to self", render.Message{From: addrs(self), To: addrs(self)}, false, self, ""},
		{"dedupe", render.Message{From: addrs("a@x"), To: addrs("A@x", self)}, true, "a@x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			to, cc := ReplyRecipients(&tt.msg, self, tt.all)
			if names(to) != tt.wantTo || names(cc) != tt.wantCc {
				t.Errorf("to=%q cc=%q, want to=%q cc=%q", names(to), names(cc), tt.wantTo, tt.wantCc)
			}
		})
	}
}

func TestReply(t *testing.T) {
	orig := &render.Message{
		From: addrs("a@x"), To: addrs(self), Subject: "RE: plan", MessageID: "m2@x",
		References: []string{"m0@x", "m1@x"}, Date: time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC), Text: "line 1\nline 2",
	}
	d, err := Reply(orig, self, "ok", false)
	if err != nil {
		t.Fatal(err)
	}
	if d.Subject != "RE: plan" || d.InReplyTo != "m2@x" || strings.Join(d.References, " ") != "m0@x m1@x m2@x" {
		t.Errorf("threading: %+v", d)
	}
	if !strings.Contains(d.Body, "ok\n\nOn Mon, 5 Oct 2026 at 09:30, a@x wrote:\n> line 1\n> line 2") {
		t.Errorf("body: %q", d.Body)
	}
	if _, err := Reply(&render.Message{}, self, "x", false); err == nil {
		t.Error("reply without Message-ID should fail")
	}
}

func TestBuild(t *testing.T) {
	raw, id, err := Build(Draft{
		From: self, To: []string{"Åsa <asa@x.se>"}, Bcc: []string{"b@x"},
		Subject: "Hej\r\nBcc: evil@x", Body: "rad 1\nrad 2", InReplyTo: "m1@x", References: []string{"m0@x", "m1@x"},
	}, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	m, err := render.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.MessageID != id || !strings.HasSuffix(id, "@proton.me") {
		t.Errorf("message id %q / %q", m.MessageID, id)
	}
	if m.Subject != "Hej Bcc: evil@x" || len(m.Bcc) != 1 || m.To[0].Name != "Åsa" {
		t.Errorf("headers: subject=%q bcc=%v to=%v", m.Subject, m.Bcc, m.To)
	}
	if m.Text != "rad 1\nrad 2" || m.InReplyTo[0] != "m1@x" || len(m.References) != 2 {
		t.Errorf("body/threading: %q %v %v", m.Text, m.InReplyTo, m.References)
	}
	if _, _, err := Build(Draft{From: self, To: []string{"not an address\r\nBcc: x@y"}}, time.Now()); err == nil {
		t.Error("invalid recipient accepted")
	}
}
