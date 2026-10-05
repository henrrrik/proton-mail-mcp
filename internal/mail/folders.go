package mail

import (
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
	err := c.do(func(conn *imapclient.Client) error {
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
