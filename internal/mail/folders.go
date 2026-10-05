package mail

import (
	"errors"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// FolderKind distinguishes Bridge's mailbox namespaces: Proton labels appear
// under "Labels/", user folders under "Folders/", everything else is a system
// folder such as INBOX, Sent or All Mail.
type FolderKind string

const (
	KindSystem FolderKind = "system"
	KindFolder FolderKind = "folder"
	KindLabel  FolderKind = "label"
)

type Folder struct {
	Name   string     `json:"name"`
	Kind   FolderKind `json:"kind"`
	Total  uint32     `json:"total"`
	Unread uint32     `json:"unread"`
}

func kindOf(name string) FolderKind {
	switch {
	case strings.HasPrefix(name, "Labels/"):
		return KindLabel
	case strings.HasPrefix(name, "Folders/"):
		return KindFolder
	}
	return KindSystem
}

// ListFolders returns every selectable mailbox for which keep returns true,
// with message and unread counts.
func (c *Client) ListFolders(keep func(name string) bool) ([]Folder, error) {
	var out []Folder
	err := c.read(func(conn *imapclient.Client) error {
		boxes, err := conn.List("", "*", nil).Collect()
		if err != nil {
			return err
		}
		out = out[:0]
		for _, b := range boxes {
			if slices.Contains(b.Attrs, imap.MailboxAttrNoSelect) || !keep(b.Mailbox) {
				continue
			}
			st, err := conn.Status(b.Mailbox, &imap.StatusOptions{NumMessages: true, NumUnseen: true}).Wait()
			if err != nil {
				return err
			}
			f := Folder{Name: b.Mailbox, Kind: kindOf(b.Mailbox)}
			if st.NumMessages != nil {
				f.Total = *st.NumMessages
			}
			if st.NumUnseen != nil {
				f.Unread = *st.NumUnseen
			}
			out = append(out, f)
		}
		return nil
	})
	return out, err
}

// Special names the folders the tools need, resolved from SPECIAL-USE
// attributes with Bridge's default names as fallback.
type Special struct {
	Drafts, AllMail, Sent, Trash, Starred string
}

// Specials resolves the special folders once and caches them.
func (c *Client) Specials() (Special, error) {
	c.specialMu.Lock()
	defer c.specialMu.Unlock()
	if c.special != nil {
		return *c.special, nil
	}
	sp := Special{Drafts: "Drafts", AllMail: "All Mail", Sent: "Sent", Trash: "Trash", Starred: "Starred"}
	err := c.read(func(conn *imapclient.Client) error {
		boxes, err := conn.List("", "*", nil).Collect()
		if err != nil {
			return err
		}
		if len(boxes) == 0 {
			// Bridge can briefly list nothing while it reconnects.
			return errors.New("no folders listed; Bridge may still be starting, try again")
		}
		for _, b := range boxes {
			for _, a := range b.Attrs {
				switch a {
				case imap.MailboxAttrDrafts:
					sp.Drafts = b.Mailbox
				case imap.MailboxAttrAll:
					sp.AllMail = b.Mailbox
				case imap.MailboxAttrSent:
					sp.Sent = b.Mailbox
				case imap.MailboxAttrTrash:
					sp.Trash = b.Mailbox
				case imap.MailboxAttrFlagged:
					sp.Starred = b.Mailbox
				}
			}
		}
		return nil
	})
	if err != nil {
		return sp, err
	}
	c.special = &sp
	return sp, nil
}

// FolderExists reports whether name is an existing, selectable mailbox,
// matched exactly (names with LIST wildcards are rejected).
func (c *Client) FolderExists(name string) (bool, error) {
	if name == "" || strings.ContainsAny(name, "*%") {
		return false, nil
	}
	found := false
	err := c.read(func(conn *imapclient.Client) error {
		boxes, err := conn.List("", name, nil).Collect()
		found = slices.ContainsFunc(boxes, func(b *imap.ListData) bool {
			return b.Mailbox == name && !slices.Contains(b.Attrs, imap.MailboxAttrNoSelect)
		})
		return err
	})
	return found, err
}
