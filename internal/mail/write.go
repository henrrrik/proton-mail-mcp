package mail

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Writes are never retried: Client.do redials only before a call, and a
// failure mid-write is returned as is.

// AppendDraft stores raw in the Drafts folder flagged \Draft and returns its
// id. The UID comes from APPENDUID when Bridge sends it, otherwise from a
// Message-ID search; it is never guessed.
func (c *Client) AppendDraft(raw []byte, messageID string) (ID, error) {
	sp, err := c.Specials()
	if err != nil {
		return ID{}, err
	}
	var id ID
	err = c.write(func(conn *imapclient.Client) error {
		cmd := conn.Append(sp.Drafts, int64(len(raw)), &imap.AppendOptions{
			Flags: []imap.Flag{imap.FlagDraft, imap.FlagSeen}, Time: time.Now(),
		})
		if _, err := cmd.Write(raw); err != nil {
			cmd.Close()
			return fmt.Errorf("append draft: %w", err)
		}
		if err := cmd.Close(); err != nil {
			return fmt.Errorf("append draft: %w", err)
		}
		data, err := cmd.Wait()
		if err != nil {
			return fmt.Errorf("append draft: %w", err)
		}
		if data.UID != 0 && data.UIDValidity != 0 {
			id = ID{sp.Drafts, data.UIDValidity, data.UID}
			return nil
		}
		sd, err := examine(conn, sp.Drafts)
		if err != nil {
			return err
		}
		res, err := conn.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: messageID}}}, nil).Wait()
		if err != nil {
			return fmt.Errorf("locate draft: %w", err)
		}
		uids := res.AllUIDs()
		if len(uids) == 0 {
			return errors.New("draft was saved but could not be located in Drafts; check the Drafts folder")
		}
		id = ID{sp.Drafts, sd.UIDValidity, slices.Max(uids)}
		return nil
	})
	return id, err
}

// DeleteDraft expunges one message from Drafts. Only messages flagged \Draft
// are touched, and only by UID, so other mail marked \Deleted is never
// expunged with it.
func (c *Client) DeleteDraft(id ID) error {
	sp, err := c.Specials()
	if err != nil {
		return err
	}
	if id.Folder != sp.Drafts {
		return fmt.Errorf("not a draft: %s is not in %s", id, sp.Drafts)
	}
	return c.write(func(conn *imapclient.Client) error {
		if !conn.Caps().Has(imap.CapUIDPlus) {
			return errors.New("server lacks UIDPLUS; refusing a folder-wide EXPUNGE")
		}
		sd, err := selectBox(conn, id.Folder, false)
		if err != nil {
			return err
		}
		if err := checkValidity(sd, id.UIDValidity); err != nil {
			return err
		}
		set := imap.UIDSetNum(id.UID)
		msgs, err := conn.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 || !(&Raw{Flags: msgs[0].Flags}).IsDraft() {
			return fmt.Errorf("refusing to delete %s: not a draft", id)
		}
		if err := conn.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
			return fmt.Errorf("mark old draft deleted: %w", err)
		}
		if err := conn.UIDExpunge(set).Close(); err != nil {
			return fmt.Errorf("expunge old draft: %w", err)
		}
		return nil
	})
}

// Result reports what a batch write did.
type Result struct {
	Updated  int      `json:"updated"`
	NotFound []string `json:"not_found,omitempty"`
}

// eachFolder selects each source folder read-write, checks UIDVALIDITY,
// drops UIDs that no longer exist (Bridge reports success for missing UIDs)
// and calls fn with the rest.
func (c *Client) eachFolder(ids []ID, fn func(conn *imapclient.Client, folder string, uids []imap.UID) error) (*Result, error) {
	res := &Result{}
	err := c.write(func(conn *imapclient.Client) error {
		for key, uids := range byFolder(ids) {
			sd, err := selectBox(conn, key.Folder, false)
			if err != nil {
				return err
			}
			if err := checkValidity(sd, key.UIDValidity); err != nil {
				return fmt.Errorf("%s: %w", key.Folder, err)
			}
			msgs, err := conn.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true}).Collect()
			if err != nil {
				return err
			}
			var present []imap.UID
			for _, m := range msgs {
				present = append(present, m.UID)
			}
			for _, u := range uids {
				if !slices.Contains(present, u) {
					res.NotFound = append(res.NotFound, ID{key.Folder, key.UIDValidity, u}.String())
				}
			}
			if len(present) == 0 {
				continue
			}
			if err := fn(conn, key.Folder, present); err != nil {
				return err
			}
			res.Updated += len(present)
		}
		return nil
	})
	return res, err
}

// Move moves messages to dest with IMAP MOVE.
func (c *Client) Move(ids []ID, dest string) (*Result, error) {
	return c.eachFolder(ids, func(conn *imapclient.Client, folder string, uids []imap.UID) error {
		if folder == dest {
			return nil
		}
		if !conn.Caps().Has(imap.CapMove) {
			// go-imap would fall back to COPY + folder-wide EXPUNGE.
			return errors.New("server lacks MOVE; refusing the COPY+EXPUNGE fallback")
		}
		if _, err := conn.Move(imap.UIDSetNum(uids...), dest).Wait(); err != nil {
			return fmt.Errorf("move %s to %s: %w", folder, dest, err)
		}
		return nil
	})
}

// AddLabel copies messages into a label folder, which is how Bridge applies
// a Proton label.
func (c *Client) AddLabel(ids []ID, label string) (*Result, error) {
	return c.eachFolder(ids, func(conn *imapclient.Client, folder string, uids []imap.UID) error {
		if folder == label {
			return nil
		}
		if _, err := conn.Copy(imap.UIDSetNum(uids...), label).Wait(); err != nil {
			return fmt.Errorf("label %s: %w", label, err)
		}
		return nil
	})
}

// RemoveLabel deletes the copies of messages inside a label folder, matched
// by Message-ID. The original messages are untouched.
func (c *Client) RemoveLabel(ids []ID, label string) (*Result, error) {
	var msgIDs []string
	res, err := c.eachFolder(ids, func(conn *imapclient.Client, _ string, uids []imap.UID) error {
		msgs, err := conn.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{Envelope: true}).Collect()
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if m.Envelope != nil && m.Envelope.MessageID != "" {
				msgIDs = append(msgIDs, m.Envelope.MessageID)
			}
		}
		return nil
	})
	if err != nil || len(msgIDs) == 0 {
		return res, err
	}
	err = c.write(func(conn *imapclient.Client) error {
		if !conn.Caps().Has(imap.CapUIDPlus) {
			return errors.New("server lacks UIDPLUS; refusing a folder-wide EXPUNGE")
		}
		if _, err := selectBox(conn, label, false); err != nil {
			return err
		}
		crits := make([]imap.SearchCriteria, len(msgIDs))
		for i, m := range msgIDs {
			crits[i] = headerCrit("Message-ID", m)
		}
		data, err := conn.UIDSearch(orAll(crits), nil).Wait()
		if err != nil {
			return err
		}
		// SEARCH HEADER is a substring match; expunge only exact Message-IDs.
		var uids []imap.UID
		if found := data.AllUIDs(); len(found) > 0 {
			msgs, err := conn.Fetch(imap.UIDSetNum(found...), &imap.FetchOptions{UID: true, Envelope: true}).Collect()
			if err != nil {
				return err
			}
			for _, m := range msgs {
				if m.Envelope != nil && slices.Contains(msgIDs, m.Envelope.MessageID) {
					uids = append(uids, m.UID)
				}
			}
		}
		if len(uids) == 0 {
			return nil
		}
		set := imap.UIDSetNum(uids...)
		if err := conn.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
			return err
		}
		return conn.UIDExpunge(set).Close()
	})
	return res, err
}

// SetFlags sets or clears read (\Seen) and starred (\Flagged) state.
func (c *Client) SetFlags(ids []ID, read, starred *bool) (*Result, error) {
	var add, remove []imap.Flag
	for _, f := range []struct {
		want *bool
		flag imap.Flag
	}{{read, imap.FlagSeen}, {starred, imap.FlagFlagged}} {
		switch {
		case f.want == nil:
		case *f.want:
			add = append(add, f.flag)
		default:
			remove = append(remove, f.flag)
		}
	}
	return c.eachFolder(ids, func(conn *imapclient.Client, _ string, uids []imap.UID) error {
		set := imap.UIDSetNum(uids...)
		for _, op := range []struct {
			op    imap.StoreFlagsOp
			flags []imap.Flag
		}{{imap.StoreFlagsAdd, add}, {imap.StoreFlagsDel, remove}} {
			if len(op.flags) == 0 {
				continue
			}
			if err := conn.Store(set, &imap.StoreFlags{Op: op.op, Silent: true, Flags: op.flags}, nil).Close(); err != nil {
				return fmt.Errorf("set flags: %w", err)
			}
		}
		return nil
	})
}

// IsDraft reports whether the message carries the \Draft flag.
func (r *Raw) IsDraft() bool {
	return slices.ContainsFunc(r.Flags, func(f imap.Flag) bool { return strings.EqualFold(string(f), string(imap.FlagDraft)) })
}
