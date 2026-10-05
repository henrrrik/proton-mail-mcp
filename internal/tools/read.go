package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/henrrrik/proton-mail-mcp/internal/mail"
	"github.com/henrrrik/proton-mail-mcp/internal/policy"
	"github.com/henrrrik/proton-mail-mcp/internal/render"
)

type listMessagesIn struct {
	Folder     string `json:"folder" jsonschema:"folder name exactly as returned by list_folders"`
	Limit      int    `json:"limit,omitempty" jsonschema:"messages per page, default 25, max 100"`
	Before     string `json:"before,omitempty" jsonschema:"next_cursor from a previous page"`
	UnreadOnly bool   `json:"unread_only,omitempty"`
}

func addListMessages(s *mcp.Server, d Deps) {
	add(s, d.Gate, policy.Read, &mcp.Tool{
		Name:        "list_messages",
		Description: "List the newest messages in a folder, newest first. Pass next_cursor as before to get older messages.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listMessagesIn) (*mcp.CallToolResult, any, error) {
		if err := d.Gate.CheckFolder(in.Folder); err != nil {
			return nil, nil, err
		}
		page, err := d.Mail.ListMessages(in.Folder, clamp(in.Limit, 25, 1, 100), in.Before, in.UnreadOnly)
		if err != nil {
			return nil, nil, err
		}
		return untrusted(in.Folder, page, "")
	})
}

type searchIn struct {
	Folder  string `json:"folder,omitempty" jsonschema:"folder to search, default All Mail"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Subject string `json:"subject,omitempty"`
	Text    string `json:"text,omitempty" jsonschema:"words anywhere in headers or body"`
	Since   string `json:"since,omitempty" jsonschema:"YYYY-MM-DD, inclusive"`
	Before  string `json:"before,omitempty" jsonschema:"YYYY-MM-DD, inclusive"`
	Limit   int    `json:"limit,omitempty" jsonschema:"default 25, max 100"`
}

func addSearch(s *mcp.Server, d Deps) {
	add(s, d.Gate, policy.Read, &mcp.Tool{
		Name:        "search",
		Description: "Search messages by sender, recipient, subject, text and date range, newest first. All given criteria must match.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
		folder := in.Folder
		if folder == "" {
			sp, err := d.Mail.Specials()
			if err != nil {
				return nil, nil, err
			}
			folder = sp.AllMail
		}
		if err := d.Gate.CheckFolder(folder); err != nil {
			return nil, nil, err
		}
		q := mail.Query{From: in.From, To: in.To, Subject: in.Subject, Text: in.Text, Limit: clamp(in.Limit, 25, 1, 100)}
		var err error
		if q.Since, err = parseDate("since", in.Since); err != nil {
			return nil, nil, err
		}
		if q.Before, err = parseDate("before", in.Before); err != nil {
			return nil, nil, err
		}
		rows, err := d.Mail.Search(folder, q)
		if err != nil {
			return nil, nil, err
		}
		return untrusted(folder, map[string]any{"messages": rows}, "")
	})
}

func parseDate(name, s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return t, fmt.Errorf("%s: want YYYY-MM-DD, got %q", name, s)
	}
	return t, nil
}

type getMessageIn struct {
	ID       string `json:"id"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"body length cap, default 20000, max 200000"`
}

func addGetMessage(s *mcp.Server, d Deps) {
	add(s, d.Gate, policy.Read, &mcp.Tool{
		Name:        "get_message",
		Description: "Read one message: headers, plain-text body and attachment list. Does not mark it as read.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getMessageIn) (*mcp.CallToolResult, any, error) {
		id, err := readable(d.Gate, in.ID)
		if err != nil {
			return nil, nil, err
		}
		raw, err := d.Mail.Message(id)
		if err != nil {
			return nil, nil, err
		}
		m, err := render.Parse(raw.Body)
		if err != nil {
			return nil, nil, err
		}
		body, cut := render.Truncate(m.Text, clamp(in.MaxChars, 20000, 1000, 200000))
		var b strings.Builder
		writeHeaders(&b, m)
		for _, a := range m.Attachments {
			fmt.Fprintf(&b, "Attachment: part %s, %q, %s, %d bytes", a.Part, render.OneLine(a.Filename), a.ContentType, a.Size)
			if a.Inline {
				b.WriteString(", inline")
			}
			b.WriteByte('\n')
		}
		b.WriteString("\n" + body)
		note := ""
		if cut {
			note = fmt.Sprintf("[body truncated at %d characters; call again with a larger max_chars to read more]", len([]rune(body)))
		}
		return untrusted(in.ID, b.String(), note)
	})
}

func writeHeaders(b *strings.Builder, m *render.Message) {
	line := func(k string, as []render.Address) {
		if len(as) == 0 {
			return
		}
		parts := make([]string, len(as))
		for i, a := range as {
			parts[i] = a.String()
		}
		fmt.Fprintf(b, "%s: %s\n", k, render.OneLine(strings.Join(parts, ", ")))
	}
	line("From", m.From)
	line("To", m.To)
	line("Cc", m.Cc)
	line("Reply-To", m.ReplyTo)
	fmt.Fprintf(b, "Subject: %s\n", render.OneLine(m.Subject))
	if !m.Date.IsZero() {
		fmt.Fprintf(b, "Date: %s\n", m.Date.Format(time.RFC1123Z))
	}
}

type idIn struct {
	ID string `json:"id"`
}

func addGetThread(s *mcp.Server, d Deps) {
	add(s, d.Gate, policy.Read, &mcp.Tool{
		Name:        "get_thread",
		Description: "Read the conversation a message belongs to, oldest first, with quoted history trimmed from each body.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		id, err := readable(d.Gate, in.ID)
		if err != nil {
			return nil, nil, err
		}
		raw, err := d.Mail.Message(id)
		if err != nil {
			return nil, nil, err
		}
		m, err := render.Parse(raw.Body)
		if err != nil {
			return nil, nil, err
		}
		folder := id.Folder
		if sp, err := d.Mail.Specials(); err == nil && d.Gate.FolderAllowed(sp.AllMail) {
			folder = sp.AllMail
		}
		root := m.MessageID
		if len(m.References) > 0 {
			root = m.References[0]
		} else if len(m.InReplyTo) > 0 {
			root = m.InReplyTo[0]
		}
		related := slices.Concat(m.References, m.InReplyTo, []string{m.MessageID})
		raws, err := d.Mail.Thread(folder, root, slices.DeleteFunc(related, func(s string) bool { return s == "" }))
		if err != nil {
			return nil, nil, err
		}
		type entry struct {
			id string
			m  *render.Message
		}
		var entries []entry
		seen := map[string]bool{}
		for _, r := range raws {
			pm, err := render.Parse(r.Body)
			if err != nil || (pm.MessageID != "" && seen[pm.MessageID]) {
				continue
			}
			seen[pm.MessageID] = true
			entries = append(entries, entry{r.ID.String(), pm})
		}
		if len(entries) == 0 {
			entries = []entry{{in.ID, m}}
		}
		slices.SortStableFunc(entries, func(a, b entry) int { return a.m.Date.Compare(b.m.Date) })
		var b strings.Builder
		for i, e := range entries {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "=== message %d of %d, id %s\n", i+1, len(entries), e.id)
			writeHeaders(&b, e.m)
			body, cut := render.Truncate(render.TrimQuoted(e.m.Text), 8000)
			b.WriteString("\n" + body + "\n")
			if cut {
				b.WriteString("[truncated; use get_message for the full body]\n")
			}
		}
		return untrusted(in.ID, b.String(), "")
	})
}

type getAttachmentIn struct {
	ID       string `json:"id"`
	Part     string `json:"part" jsonschema:"attachment part number from get_message, e.g. 2 or 1.2"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"default 20000, max 200000"`
}

func addGetAttachment(s *mcp.Server, d Deps) {
	add(s, d.Gate, policy.Read, &mcp.Tool{
		Name:        "get_attachment",
		Description: "Read an attachment's text (text, CSV, JSON, HTML, PDF and similar). Binary attachments return metadata only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getAttachmentIn) (*mcp.CallToolResult, any, error) {
		id, err := readable(d.Gate, in.ID)
		if err != nil {
			return nil, nil, err
		}
		raw, err := d.Mail.Message(id)
		if err != nil {
			return nil, nil, err
		}
		body, ct, name, err := render.Part(raw.Body, in.Part)
		if err != nil {
			return nil, nil, err
		}
		header := fmt.Sprintf("Attachment %q, %s, %d bytes\n\n", name, ct, len(body))
		text, ok, err := render.AttachmentText(body, ct, name)
		if err != nil {
			return untrusted(in.ID, header+"[could not extract text: "+err.Error()+"]", "")
		}
		if !ok {
			return untrusted(in.ID, header+"[binary attachment; content not returned]", "")
		}
		text, cut := render.Truncate(text, clamp(in.MaxChars, 20000, 1000, 200000))
		note := ""
		if cut {
			note = "[attachment text truncated; call again with a larger max_chars to read more]"
		}
		return untrusted(in.ID, header+text, note)
	})
}
