package mail

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
)

var (
	ErrStale    = errors.New("stale id: the folder was rebuilt since this id was issued (UIDVALIDITY changed); list or search again")
	ErrNotFound = errors.New("message not found: it may have been moved or deleted; list or search again")
	ErrBadID    = errors.New(`invalid message id: expected "folder:uidvalidity:uid" as returned by list_messages or search`)
)

// ID addresses one message. UIDVALIDITY is part of it so an id from before
// a mailbox rebuild fails loudly instead of hitting a different message.
type ID struct {
	Folder      string
	UIDValidity uint32
	UID         imap.UID
}

func (id ID) String() string {
	return fmt.Sprintf("%s:%d:%d", id.Folder, id.UIDValidity, id.UID)
}

// ParseID parses "folder:uidvalidity:uid". Folder names may contain ':', so
// the numbers are taken from the right.
func ParseID(s string) (ID, error) {
	j := strings.LastIndexByte(s, ':')
	if j <= 0 {
		return ID{}, ErrBadID
	}
	i := strings.LastIndexByte(s[:j], ':')
	if i <= 0 {
		return ID{}, ErrBadID
	}
	v, err1 := strconv.ParseUint(s[i+1:j], 10, 32)
	u, err2 := strconv.ParseUint(s[j+1:], 10, 32)
	if err1 != nil || err2 != nil || u == 0 {
		return ID{}, ErrBadID
	}
	return ID{Folder: s[:i], UIDValidity: uint32(v), UID: imap.UID(u)}, nil
}

// ParseIDs parses a list of ids, keeping order.
func ParseIDs(ss []string) ([]ID, error) {
	out := make([]ID, 0, len(ss))
	for _, s := range ss {
		id, err := ParseID(s)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", s, err)
		}
		out = append(out, id)
	}
	return out, nil
}

// byFolder groups ids by folder and UIDVALIDITY.
func byFolder(ids []ID) map[ID][]imap.UID {
	groups := map[ID][]imap.UID{}
	for _, id := range ids {
		k := ID{Folder: id.Folder, UIDValidity: id.UIDValidity}
		groups[k] = append(groups[k], id.UID)
	}
	return groups
}
