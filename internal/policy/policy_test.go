package policy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyRefusesWrites(t *testing.T) {
	g, err := New(Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Allow(Read); err != nil {
		t.Errorf("Read refused: %v", err)
	}
	for _, c := range []Class{Draft, Modify} {
		if err := g.Allow(c); !errors.Is(err, ErrReadOnly) {
			t.Errorf("Allow(%s) = %v, want ErrReadOnly", c, err)
		}
	}

	g, _ = New(Options{ReadOnly: false})
	for _, c := range []Class{Read, Draft, Modify} {
		if err := g.Allow(c); err != nil {
			t.Errorf("Allow(%s) = %v with read-only off", c, err)
		}
	}
}

func TestFolderRules(t *testing.T) {
	tests := []struct {
		name   string
		opts   Options
		folder string
		want   bool
	}{
		{"no rules", Options{}, "Spam", true},
		{"denied", Options{DenyFolders: []string{"Spam"}}, "Spam", false},
		{"not in allowlist", Options{AllowFolders: []string{"INBOX"}}, "Sent", false},
		{"inbox any case", Options{AllowFolders: []string{"INBOX"}}, "Inbox", true},
		{"label allowed", Options{AllowFolders: []string{"INBOX", "Labels/Work"}}, "Labels/Work", true},
		{"other names exact", Options{AllowFolders: []string{"Labels/Work"}}, "labels/work", false},
		{"deny wins", Options{AllowFolders: []string{"Spam"}, DenyFolders: []string{"Spam"}}, "Spam", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, _ := New(tt.opts)
			if got := g.FolderAllowed(tt.folder); got != tt.want {
				t.Errorf("FolderAllowed(%q) = %v, want %v", tt.folder, got, tt.want)
			}
			if err := g.CheckFolder(tt.folder); (err == nil) != tt.want {
				t.Errorf("CheckFolder(%q) = %v", tt.folder, err)
			}
		})
	}
}

func TestCheckBatch(t *testing.T) {
	g, _ := New(Options{})
	if err := g.CheckBatch(MaxBatch); err != nil {
		t.Error(err)
	}
	if err := g.CheckBatch(MaxBatch + 1); !errors.Is(err, ErrBatchTooBig) {
		t.Errorf("err = %v", err)
	}
}

func TestAuditLogsWritesOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	g, err := New(Options{AuditLog: path})
	if err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }

	if err := g.Audit("get_message", Read, []string{"INBOX:1:2"}, "INBOX", nil); err != nil {
		t.Fatal(err)
	}
	if err := g.Audit("move", Modify, []string{"INBOX:1:2"}, "Archive", nil); err != nil {
		t.Fatal(err)
	}
	if err := g.Audit("set_flags", Modify, []string{"INBOX:1:3"}, "", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), b)
	}
	var e AuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Tool != "move" || e.Class != "modify" || e.Folder != "Archive" || !e.OK {
		t.Errorf("entry = %+v", e)
	}
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil {
		t.Fatal(err)
	}
	if e.OK || e.Error != "boom" {
		t.Errorf("failed entry = %+v", e)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("audit log mode = %v", fi.Mode().Perm())
	}
}
