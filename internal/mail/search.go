package mail

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"golang.org/x/text/unicode/norm"

	"github.com/henrrrik/proton-mail-mcp/internal/render"
)

type Query struct {
	From, To, Subject, Text string
	Since, Before           time.Time // dates only; Before is inclusive
	Limit                   int
}

// maxLocalCandidates bounds client-side verification when a query has
// non-ASCII text that Bridge cannot match.
const maxLocalCandidates = 500

// Search runs an IMAP SEARCH in folder, newest results first, deduplicated by
// Message-ID. Bridge's SEARCH never matches non-ASCII values, so such values
// are narrowed to their longest ASCII run for the server and the candidates
// are then verified locally, ignoring case and accents.
func (c *Client) Search(folder string, q Query) ([]Header, error) {
	crit := &imap.SearchCriteria{Since: q.Since}
	if !q.Before.IsZero() {
		crit.Before = q.Before.AddDate(0, 0, 1) // IMAP BEFORE is exclusive
	}
	local := false
	add := func(key, val string) {
		if val == "" {
			return
		}
		server := val
		if !isASCII(val) {
			local = true
			server = longestASCIIRun(val)
			if len(server) < 2 {
				return
			}
		}
		if key == "TEXT" {
			crit.Text = append(crit.Text, server)
		} else {
			crit.Header = append(crit.Header, imap.SearchCriteriaHeaderField{Key: key, Value: server})
		}
	}
	add("From", q.From)
	add("To", q.To)
	add("Subject", q.Subject)
	add("TEXT", q.Text)

	var out []Header
	err := c.read(func(conn *imapclient.Client) error {
		out = nil
		sd, err := examine(conn, folder)
		if err != nil {
			return err
		}
		data, err := conn.UIDSearch(crit, nil).Wait()
		if err != nil {
			return fmt.Errorf("search %q: %w", folder, err)
		}
		uids := data.AllUIDs()
		slices.Sort(uids)
		take := q.Limit
		if local {
			take = maxLocalCandidates
		}
		uids = uids[max(0, len(uids)-take):]
		hs, err := fetchHeaders(conn, folder, sd.UIDValidity, uids)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, h := range hs {
			if len(out) == q.Limit {
				break
			}
			if h.MessageID != "" {
				if seen[h.MessageID] {
					continue
				}
				seen[h.MessageID] = true
			}
			if local && !matchHeader(h, q) {
				continue
			}
			if local && q.Text != "" && !isASCII(q.Text) {
				id, _ := ParseID(h.ID)
				raw, err := fetchRaw(conn, id, true)
				if err != nil || !matchText(raw.Body, h, q.Text) {
					continue
				}
			}
			out = append(out, h)
		}
		return nil
	})
	if out == nil {
		out = []Header{}
	}
	return out, err
}

func matchHeader(h Header, q Query) bool {
	return contains(addrText(h.From), q.From) && contains(addrText(h.To), q.To) && contains(h.Subject, q.Subject)
}

func matchText(raw []byte, h Header, text string) bool {
	m, err := render.Parse(raw)
	if err != nil {
		return false
	}
	return contains(h.Subject+"\n"+addrText(h.From)+"\n"+m.Text, text)
}

func addrText(as []render.Address) string {
	var b strings.Builder
	for _, a := range as {
		b.WriteString(a.Name + " " + a.Address + " ")
	}
	return b.String()
}

func contains(haystack, needle string) bool {
	return needle == "" || strings.Contains(Fold(haystack), Fold(needle))
}

var foldSpecial = strings.NewReplacer("ß", "ss", "æ", "ae", "œ", "oe", "ø", "o", "ł", "l", "đ", "d", "ð", "d", "þ", "th", "ı", "i")

// Fold lowercases s and strips accents, so "Räkning" matches "rakning".
func Fold(s string) string {
	s = foldSpecial.Replace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range norm.NFKD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// longestASCIIRun returns the longest run of ASCII letters and digits, which
// is a substring of every true match of s.
func longestASCIIRun(s string) string {
	best, cur := "", ""
	for _, r := range s {
		if r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			cur += string(r)
			if len(cur) > len(best) {
				best = cur
			}
		} else {
			cur = ""
		}
	}
	return best
}
