package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/henrrrik/proton-mail-mcp/internal/mail"
	"github.com/henrrrik/proton-mail-mcp/internal/policy"
)

type moveIn struct {
	IDs         []string `json:"ids" jsonschema:"message ids, at most 50"`
	Destination string   `json:"destination" jsonschema:"folder name from list_folders, e.g. Archive, Trash or Folders/Receipts"`
}

func (in moveIn) audit() ([]string, string) { return in.IDs, in.Destination }

type setLabelsIn struct {
	IDs    []string `json:"ids" jsonschema:"message ids, at most 50"`
	Add    []string `json:"add,omitempty" jsonschema:"label names, with or without the Labels/ prefix"`
	Remove []string `json:"remove,omitempty"`
}

func (in setLabelsIn) audit() ([]string, string) {
	return in.IDs, "add=" + strings.Join(in.Add, ",") + " remove=" + strings.Join(in.Remove, ",")
}

type setFlagsIn struct {
	IDs     []string `json:"ids" jsonschema:"message ids, at most 50"`
	Read    *bool    `json:"read,omitempty"`
	Starred *bool    `json:"starred,omitempty"`
}

func (in setFlagsIn) audit() ([]string, string) { return in.IDs, "" }

func addOrganiseTools(s *mcp.Server, d Deps) {
	add(s, d.Gate, policy.Modify, &mcp.Tool{
		Name:        "move",
		Description: "Move messages to another folder. To delete, move to Trash. Use set_labels for labels.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in moveIn) (*mcp.CallToolResult, *mail.Result, error) {
		ids, err := writable(d.Gate, in.IDs)
		if err != nil {
			return nil, nil, err
		}
		sp, err := d.Mail.Specials()
		if err != nil {
			return nil, nil, err
		}
		if err := checkMove(d, sp, ids, in.Destination); err != nil {
			return nil, nil, err
		}
		res, err := d.Mail.Move(ids, in.Destination)
		return nil, res, err
	})

	add(s, d.Gate, policy.Modify, &mcp.Tool{
		Name:        "set_labels",
		Description: "Add or remove Proton labels on messages. Labels must already exist.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setLabelsIn) (*mcp.CallToolResult, *mail.Result, error) {
		ids, err := writable(d.Gate, in.IDs)
		if err != nil {
			return nil, nil, err
		}
		if len(in.Add)+len(in.Remove) == 0 {
			return nil, nil, errors.New("nothing to do: give add or remove")
		}
		total := &mail.Result{}
		for _, op := range []struct {
			labels []string
			fn     func([]mail.ID, string) (*mail.Result, error)
		}{{in.Add, d.Mail.AddLabel}, {in.Remove, d.Mail.RemoveLabel}} {
			for _, l := range op.labels {
				label := labelFolder(l)
				if err := mustExist(d, label); err != nil {
					return nil, nil, err
				}
				res, err := op.fn(ids, label)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", label, err)
				}
				total.Updated = max(total.Updated, res.Updated)
				total.NotFound = res.NotFound
			}
		}
		return nil, total, nil
	})

	add(s, d.Gate, policy.Modify, &mcp.Tool{
		Name:        "set_flags",
		Description: "Mark messages read or unread, starred or unstarred.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setFlagsIn) (*mcp.CallToolResult, *mail.Result, error) {
		ids, err := writable(d.Gate, in.IDs)
		if err != nil {
			return nil, nil, err
		}
		if in.Read == nil && in.Starred == nil {
			return nil, nil, errors.New("nothing to do: give read or starred")
		}
		res, err := d.Mail.SetFlags(ids, in.Read, in.Starred)
		return nil, res, err
	})
}

// checkMove refuses moves Bridge would interpret surprisingly: into views
// (labels, All Mail, Starred), into Drafts or Sent, or out of a label view.
func checkMove(d Deps, sp mail.Special, ids []mail.ID, dest string) error {
	if err := mustExist(d, dest); err != nil {
		return err
	}
	switch {
	case strings.HasPrefix(dest, "Labels/"):
		return errors.New("labels are not folders here; use set_labels")
	case dest == sp.AllMail || dest == sp.Starred || dest == sp.Drafts || dest == sp.Sent:
		return fmt.Errorf("cannot move into %s", dest)
	}
	for _, id := range ids {
		if strings.HasPrefix(id.Folder, "Labels/") || id.Folder == sp.Starred {
			return fmt.Errorf("%s comes from the %s view; use the id from its folder or from %s instead", id, id.Folder, sp.AllMail)
		}
	}
	return nil
}

func labelFolder(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "Labels/") {
		return name
	}
	return "Labels/" + name
}

// mustExist checks a destination or label against policy and against the
// exact mailbox list, so crafted names never reach SELECT, COPY or EXPUNGE.
func mustExist(d Deps, name string) error {
	if err := d.Gate.CheckFolder(name); err != nil {
		return err
	}
	ok, err := d.Mail.FolderExists(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no folder or label named %q; see list_folders", name)
	}
	return nil
}
