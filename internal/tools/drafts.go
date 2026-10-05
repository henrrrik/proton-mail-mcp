package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/henrrrik/proton-mail-mcp/internal/draft"
	"github.com/henrrrik/proton-mail-mcp/internal/mail"
	"github.com/henrrrik/proton-mail-mcp/internal/policy"
	"github.com/henrrrik/proton-mail-mcp/internal/render"
)

type draftOut struct {
	DraftID    string   `json:"draft_id"`
	Warning    string   `json:"warning,omitempty"`
	recipients []string // audit only
}

func (o draftOut) auditResult() any {
	return map[string]any{"draft_id": o.DraftID, "recipients": o.recipients, "warning": o.Warning}
}

type createDraftIn struct {
	To        []string `json:"to,omitempty" jsonschema:"recipients, e.g. Anna <anna@example.com>"`
	Cc        []string `json:"cc,omitempty"`
	Bcc       []string `json:"bcc,omitempty"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body" jsonschema:"plain text or markdown"`
	InReplyTo string   `json:"in_reply_to,omitempty" jsonschema:"id of a message this draft replies to, to thread it without quoting"`
}

func (in createDraftIn) audit() ([]string, string) { return nonEmpty(in.InReplyTo), "Drafts" }

type updateDraftIn struct {
	ID      string    `json:"id" jsonschema:"draft id from create_draft or a previous update_draft"`
	To      *[]string `json:"to,omitempty"`
	Cc      *[]string `json:"cc,omitempty"`
	Bcc     *[]string `json:"bcc,omitempty"`
	Subject *string   `json:"subject,omitempty"`
	Body    *string   `json:"body,omitempty"`
}

func (in updateDraftIn) audit() ([]string, string) { return nonEmpty(in.ID), "Drafts" }

type replyDraftIn struct {
	ID       string `json:"id" jsonschema:"id of the message to reply to"`
	Body     string `json:"body" jsonschema:"your reply; the original is quoted below it"`
	ReplyAll bool   `json:"reply_all,omitempty"`
}

func (in replyDraftIn) audit() ([]string, string) { return nonEmpty(in.ID), "Drafts" }

const draftNote = " The draft is saved to Drafts for the user to review and send from the Proton app; it is not sent."

func addDraftTools(s *mcp.Server, d Deps) {
	add(s, d.Gate, policy.Draft, &mcp.Tool{
		Name:        "create_draft",
		Description: "Save a new draft." + draftNote,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createDraftIn) (*mcp.CallToolResult, draftOut, error) {
		dr := draft.Draft{From: d.Self, To: in.To, Cc: in.Cc, Bcc: in.Bcc, Subject: in.Subject, Body: in.Body}
		if in.InReplyTo != "" {
			orig, err := original(d, in.InReplyTo)
			if err != nil {
				return nil, draftOut{}, err
			}
			dr.InReplyTo = orig.MessageID
			dr.References = append(append([]string{}, orig.References...), orig.MessageID)
		}
		return saveDraft(d, dr, nil)
	})

	add(s, d.Gate, policy.Draft, &mcp.Tool{
		Name:        "update_draft",
		Description: "Change fields of an existing draft. Omitted fields are kept. Returns the draft's new id; the old id stops working.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateDraftIn) (*mcp.CallToolResult, draftOut, error) {
		id, err := readable(d.Gate, in.ID)
		if err != nil {
			return nil, draftOut{}, err
		}
		sp, err := d.Mail.Specials()
		if err != nil {
			return nil, draftOut{}, err
		}
		if id.Folder != sp.Drafts {
			return nil, draftOut{}, fmt.Errorf("%s is not in %s; only drafts can be updated", in.ID, sp.Drafts)
		}
		raw, err := d.Mail.Message(id)
		if err != nil {
			return nil, draftOut{}, err
		}
		if !raw.IsDraft() {
			return nil, draftOut{}, errors.New("that message is not a draft")
		}
		m, err := render.Parse(raw.Body)
		if err != nil {
			return nil, draftOut{}, err
		}
		dr := draft.FromMessage(m, m.Text)
		dr.From = d.Self
		set(&dr.To, in.To)
		set(&dr.Cc, in.Cc)
		set(&dr.Bcc, in.Bcc)
		set(&dr.Subject, in.Subject)
		set(&dr.Body, in.Body)
		return saveDraft(d, dr, &id)
	})

	add(s, d.Gate, policy.Draft, &mcp.Tool{
		Name:        "create_reply_draft",
		Description: "Save a reply to a message as a draft, threaded and with the original quoted. Replies to your own sent mail go to its original recipients." + draftNote,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in replyDraftIn) (*mcp.CallToolResult, draftOut, error) {
		orig, err := original(d, in.ID)
		if err != nil {
			return nil, draftOut{}, err
		}
		dr, err := draft.Reply(orig, d.Self, in.Body, in.ReplyAll)
		if err != nil {
			return nil, draftOut{}, err
		}
		res, out, err := saveDraft(d, dr, nil)
		if len(orig.ReplyTo) > 0 && len(orig.From) > 0 && !draft.IsSelf(orig.From[0].Address, d.Self) &&
			!strings.EqualFold(orig.ReplyTo[0].Address, orig.From[0].Address) {
			// Reply-To is set by the sender and may point anywhere.
			out.Warning = render.Escape(render.OneLine(fmt.Sprintf("addressed to the Reply-To address %s, not the sender %s; check before sending",
				orig.ReplyTo[0].Address, orig.From[0].Address)))
		}
		return res, out, err
	})
}

func original(d Deps, s string) (*render.Message, error) {
	id, err := readable(d.Gate, s)
	if err != nil {
		return nil, err
	}
	raw, err := d.Mail.Message(id)
	if err != nil {
		return nil, err
	}
	return render.Parse(raw.Body)
}

// saveDraft appends the new draft, then removes the one it replaces. The new
// id is returned even if removing the old copy fails, so it is never lost.
func saveDraft(d Deps, dr draft.Draft, replaces *mail.ID) (*mcp.CallToolResult, draftOut, error) {
	sp, err := d.Mail.Specials()
	if err != nil {
		return nil, draftOut{}, err
	}
	if err := d.Gate.CheckFolder(sp.Drafts); err != nil {
		return nil, draftOut{}, err
	}
	raw, msgID, err := draft.Build(dr, time.Now())
	if err != nil {
		return nil, draftOut{}, err
	}
	id, err := d.Mail.AppendDraft(raw, msgID)
	if err != nil {
		return nil, draftOut{}, err
	}
	out := draftOut{DraftID: id.String()}
	for _, list := range [][]string{dr.To, dr.Cc, dr.Bcc} {
		addrs, _ := draft.ParseAddresses(list)
		for _, a := range addrs {
			out.recipients = append(out.recipients, a.Address)
		}
	}
	if replaces != nil {
		if err := d.Mail.DeleteDraft(*replaces); err != nil {
			out.Warning = "the new draft was saved, but the previous version could not be removed: " + err.Error()
		}
	}
	return nil, out, nil
}

func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
