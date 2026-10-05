package mail

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/henrrrik/proton-mail-mcp/internal/render"
)

// Header is one row of a message listing. Strings are email-derived.
type Header struct {
	ID             string           `json:"id"`
	MessageID      string           `json:"-"`
	From           []render.Address `json:"from"`
	To             []render.Address `json:"to"`
	Subject        string           `json:"subject"`
	Date           time.Time        `json:"date"`
	Flags          []string         `json:"flags"`
	HasAttachments bool             `json:"has_attachments"`
}

// Page is a listing plus the cursor for the next (older) page, if any.
type Page struct {
	Messages []Header `json:"messages"`
	Next     string   `json:"next_cursor,omitempty"`
}

// Raw is a whole message as stored.
type Raw struct {
	ID    ID
	Flags []imap.Flag
	Body  []byte
}

var fullBody = &imap.FetchItemBodySection{Peek: true} // PEEK: reading must not mark as read

func examine(conn *imapclient.Client, folder string) (*imap.SelectData, error) {
	return selectBox(conn, folder, true)
}

func selectBox(conn *imapclient.Client, folder string, readOnly bool) (*imap.SelectData, error) {
	sd, err := conn.Select(folder, &imap.SelectOptions{ReadOnly: readOnly}).Wait()
	if err != nil {
		return nil, fmt.Errorf("open folder %q: %w", folder, err)
	}
	return sd, nil
}

func checkValidity(sd *imap.SelectData, want uint32) error {
	if sd.UIDValidity != want {
		return ErrStale
	}
	return nil
}

// ListMessages returns the newest messages in folder, paging backwards by
// UID. cursor is a Page.Next value from a previous call, or empty.
func (c *Client) ListMessages(folder string, limit int, cursor string, unreadOnly bool) (*Page, error) {
	var page *Page
	err := c.read(func(conn *imapclient.Client) error {
		sd, err := examine(conn, folder)
		if err != nil {
			return err
		}
		crit := &imap.SearchCriteria{}
		if cursor != "" {
			v, before, err := parseCursor(cursor)
			if err != nil {
				return err
			}
			if err := checkValidity(sd, v); err != nil {
				return err
			}
			if before <= 1 {
				page = &Page{Messages: []Header{}}
				return nil
			}
			crit.UID = []imap.UIDSet{{imap.UIDRange{Start: 1, Stop: before - 1}}}
		}
		if unreadOnly {
			crit.NotFlag = []imap.Flag{imap.FlagSeen}
		}
		data, err := conn.UIDSearch(crit, nil).Wait()
		if err != nil {
			return fmt.Errorf("search %q: %w", folder, err)
		}
		uids := data.AllUIDs()
		slices.Sort(uids)
		page = &Page{}
		if len(uids) > limit {
			uids = uids[len(uids)-limit:]
			page.Next = fmt.Sprintf("%d:%d", sd.UIDValidity, uids[0])
		}
		page.Messages, err = fetchHeaders(conn, folder, sd.UIDValidity, uids)
		return err
	})
	return page, err
}

func parseCursor(s string) (uint32, imap.UID, error) {
	v, u, ok := strings.Cut(s, ":")
	vv, err1 := strconv.ParseUint(v, 10, 32)
	uu, err2 := strconv.ParseUint(u, 10, 32)
	if !ok || err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("invalid cursor %q", s)
	}
	return uint32(vv), imap.UID(uu), nil
}

// fetchHeaders fetches listing rows for uids in the selected folder, newest
// first by date (UID order does not follow date order after imports).
func fetchHeaders(conn *imapclient.Client, folder string, validity uint32, uids []imap.UID) ([]Header, error) {
	out := []Header{}
	if len(uids) == 0 {
		return out, nil
	}
	msgs, err := conn.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID: true, Envelope: true, Flags: true, InternalDate: true, BodyStructure: &imap.FetchItemBodyStructure{},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch headers: %w", err)
	}
	for _, m := range msgs {
		h := Header{ID: ID{folder, validity, m.UID}.String(), Date: m.InternalDate, Flags: flagNames(m.Flags)}
		if env := m.Envelope; env != nil {
			h.MessageID = env.MessageID
			h.Subject = env.Subject
			h.From = addrs(env.From)
			h.To = addrs(env.To)
			if !env.Date.IsZero() {
				h.Date = env.Date
			}
		}
		h.HasAttachments = hasAttachments(m.BodyStructure)
		out = append(out, h)
	}
	slices.SortStableFunc(out, func(a, b Header) int { return b.Date.Compare(a.Date) })
	return out, nil
}

// Message fetches a whole message without marking it read.
func (c *Client) Message(id ID) (*Raw, error) {
	var raw *Raw
	err := c.read(func(conn *imapclient.Client) error {
		sd, err := examine(conn, id.Folder)
		if err != nil {
			return err
		}
		if err := checkValidity(sd, id.UIDValidity); err != nil {
			return err
		}
		raw, err = fetchRaw(conn, id, false)
		return err
	})
	return raw, err
}

// MaxMessageBytes is the largest message read whole.
const MaxMessageBytes = 32 << 20

// headBytes is how much of each message is read when only the start
// matters (thread bodies, local search checks).
const headBytes = 1 << 20

// fetchRaw fetches a message, or only its first head bytes when head is set.
// Oversized messages are refused before their body is downloaded.
func fetchRaw(conn *imapclient.Client, id ID, head bool) (*Raw, error) {
	set := imap.UIDSetNum(id.UID)
	meta, err := conn.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, RFC822Size: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch message: %w", err)
	}
	if len(meta) == 0 {
		return nil, ErrNotFound
	}
	section := fullBody
	if head {
		section = &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Size: headBytes}}
	} else if meta[0].RFC822Size > MaxMessageBytes {
		return nil, fmt.Errorf("message is %d MB, more than the %d MB this server reads", meta[0].RFC822Size>>20, MaxMessageBytes>>20)
	}
	msgs, err := conn.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch message: %w", err)
	}
	if len(msgs) == 0 {
		return nil, ErrNotFound
	}
	msgs[0].Flags = meta[0].Flags
	body := msgs[0].FindBodySection(section)
	if body == nil {
		return nil, ErrNotFound
	}
	return &Raw{ID: id, Flags: msgs[0].Flags, Body: body}, nil
}

// MaxThread caps how many messages a thread lookup returns.
const MaxThread = 50

// Thread finds messages in folder that belong to the same conversation: the
// messages named by msgIDs, plus any message whose References mention root.
// Bridge has no THREAD extension, so this is a single OR'd header search.
func (c *Client) Thread(folder, root string, msgIDs []string) ([]*Raw, error) {
	var crits []imap.SearchCriteria
	if root != "" {
		crits = append(crits, headerCrit("References", root))
	}
	for _, m := range msgIDs[:min(len(msgIDs), 20)] {
		crits = append(crits, headerCrit("Message-ID", m))
	}
	if len(crits) == 0 {
		return nil, nil
	}
	var out []*Raw
	err := c.read(func(conn *imapclient.Client) error {
		out = nil
		sd, err := examine(conn, folder)
		if err != nil {
			return err
		}
		data, err := conn.UIDSearch(orAll(crits), nil).Wait()
		if err != nil {
			return fmt.Errorf("thread search: %w", err)
		}
		uids := data.AllUIDs()
		slices.Sort(uids)
		uids = uids[max(0, len(uids)-MaxThread):]
		for _, uid := range uids {
			r, err := fetchRaw(conn, ID{folder, sd.UIDValidity, uid}, true)
			if err == ErrNotFound {
				continue
			}
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return nil
	})
	return out, err
}

func headerCrit(key, msgID string) imap.SearchCriteria {
	return imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: key, Value: "<" + strings.Trim(msgID, "<>") + ">"}}}
}

// orAll combines criteria with nested ORs.
func orAll(cs []imap.SearchCriteria) *imap.SearchCriteria {
	if len(cs) == 1 {
		return &cs[0]
	}
	mid := len(cs) / 2
	return &imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{*orAll(cs[:mid]), *orAll(cs[mid:])}}}
}

func addrs(in []imap.Address) []render.Address {
	out := []render.Address{}
	for _, a := range in {
		if addr := a.Addr(); addr != "" {
			out = append(out, render.Address{Name: a.Name, Address: addr})
		}
	}
	return out
}

func flagNames(fs []imap.Flag) []string {
	out := []string{}
	for _, f := range fs {
		switch f {
		case imap.FlagSeen:
			out = append(out, "read")
		case imap.FlagFlagged:
			out = append(out, "starred")
		case imap.FlagAnswered:
			out = append(out, "answered")
		case imap.FlagDraft:
			out = append(out, "draft")
		}
	}
	return out
}

func hasAttachments(bs imap.BodyStructure) bool {
	found := false
	if bs == nil {
		return false
	}
	bs.Walk(func(_ []int, part imap.BodyStructure) bool {
		if sp, ok := part.(*imap.BodyStructureSinglePart); ok {
			if d := sp.Disposition(); (d != nil && strings.EqualFold(d.Value, "attachment")) || (sp.Filename() != "" && sp.Type != "text") {
				found = true
			}
		}
		return !found
	})
	return found
}
