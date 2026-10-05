// Package policy is the single gate every tool call passes through before it
// touches IMAP. All safety rules live here: read-only mode, folder
// allow/deny lists, batch caps and the audit log.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Class is what a tool is allowed to do.
type Class int

const (
	Read   Class = iota // reads mail, changes nothing
	Draft               // writes to the Drafts folder only
	Modify              // moves, labels or flags existing messages
)

func (c Class) String() string {
	switch c {
	case Read:
		return "read"
	case Draft:
		return "draft"
	case Modify:
		return "modify"
	}
	return fmt.Sprintf("Class(%d)", int(c))
}

// MaxBatch caps how many message ids a single write call may touch.
const MaxBatch = 50

var (
	ErrReadOnly     = errors.New("refused: server is read-only (set READ_ONLY=false to allow drafts and organising)")
	ErrFolderDenied = errors.New("refused: folder not permitted by policy")
	ErrBatchTooBig  = fmt.Errorf("refused: at most %d ids per call", MaxBatch)
)

type Options struct {
	ReadOnly     bool
	AllowFolders []string
	DenyFolders  []string
	AuditLog     string // path; empty disables auditing
}

type Gate struct {
	readOnly bool
	allow    []string
	deny     []string

	mu    sync.Mutex
	audit *os.File
	now   func() time.Time
}

func New(o Options) (*Gate, error) {
	g := &Gate{readOnly: o.ReadOnly, allow: o.AllowFolders, deny: o.DenyFolders, now: time.Now}
	if o.AuditLog != "" {
		f, err := os.OpenFile(o.AuditLog, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open audit log: %w", err)
		}
		g.audit = f
	}
	return g, nil
}

func (g *Gate) Close() error {
	if g.audit == nil {
		return nil
	}
	return g.audit.Close()
}

// ReadOnly reports whether write tools are disabled.
func (g *Gate) ReadOnly() bool { return g.readOnly }

// Allow checks whether a tool of the given class may run at all.
func (g *Gate) Allow(c Class) error {
	if c != Read && g.readOnly {
		return ErrReadOnly
	}
	return nil
}

// FolderAllowed reports whether a mailbox may be read or written. The deny
// list wins over the allow list; an empty allow list permits everything.
//
// Bridge's views (All Mail, Starred and every label) show messages that
// live in other folders, so they could expose denied mail. Whenever a deny
// list is set, views are refused unless the allow list names them.
func (g *Gate) FolderAllowed(name string) bool {
	if slices.ContainsFunc(g.deny, func(d string) bool { return sameFolder(d, name) }) {
		return false
	}
	allowed := slices.ContainsFunc(g.allow, func(a string) bool { return sameFolder(a, name) })
	if len(g.deny) > 0 && isView(name) {
		return allowed
	}
	return len(g.allow) == 0 || allowed
}

func isView(name string) bool {
	return name == "All Mail" || name == "Starred" || strings.HasPrefix(name, "Labels/")
}

// CheckFolder is FolderAllowed as an error.
func (g *Gate) CheckFolder(name string) error {
	if !g.FolderAllowed(name) {
		return fmt.Errorf("%w: %q", ErrFolderDenied, name)
	}
	return nil
}

// CheckBatch rejects write calls that touch too many ids.
func (g *Gate) CheckBatch(n int) error {
	if n > MaxBatch {
		return ErrBatchTooBig
	}
	return nil
}

// AuditEntry describes one write call. It must never carry message bodies.
// Each call is logged twice: phase "start" before it runs, so a crash still
// leaves a trace, and phase "done" with the outcome.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Tool   string    `json:"tool"`
	Class  string    `json:"class"`
	Phase  string    `json:"phase"`
	IDs    []string  `json:"ids,omitempty"`
	Folder string    `json:"folder,omitempty"`
	Result any       `json:"result,omitempty"`
	Error  string    `json:"error,omitempty"`
}

// Audit appends one JSON line for a write call. Read calls are not logged.
func (g *Gate) Audit(c Class, e AuditEntry) error {
	if g.audit == nil || c == Read {
		return nil
	}
	e.Time, e.Class = g.now().UTC(), c.String()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, err = g.audit.Write(append(b, '\n'))
	return err
}

// INBOX is case-insensitive in IMAP; every other name is compared exactly.
func sameFolder(a, b string) bool {
	if strings.EqualFold(a, "INBOX") {
		return strings.EqualFold(b, "INBOX")
	}
	return a == b
}
