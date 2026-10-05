package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/henrrrik/proton-mail-mcp/internal/mail"
	"github.com/henrrrik/proton-mail-mcp/internal/mail/mailtest"
	"github.com/henrrrik/proton-mail-mcp/internal/policy"
	"github.com/henrrrik/proton-mail-mcp/internal/tools"
)

// session wires a server backed by the in-memory IMAP server to an MCP client.
func session(t *testing.T, opts policy.Options) (*mcp.ClientSession, *mailtest.Server) {
	t.Helper()
	srv := mailtest.Start(t)
	tlsCfg, err := mail.TLSConfig(srv.CertPath, "127.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	mc := mail.New(mail.Options{Addr: srv.Addr, User: mailtest.User, Password: mailtest.Password, TLS: tlsCfg})
	t.Cleanup(func() { mc.Close() })
	gate, err := policy.New(opts)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	server := tools.NewServer("test", tools.Deps{Gate: gate, Mail: mc})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, srv
}

func TestListFoldersTool(t *testing.T) {
	cs, srv := session(t, policy.Options{ReadOnly: true, DenyFolders: []string{"Spam"}})
	srv.Append(t, "INBOX", "Subject: hi\n\nhello")

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_folders"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var out struct{ Folders []mail.Folder }
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	names := map[string]mail.Folder{}
	for _, f := range out.Folders {
		names[f.Name] = f
	}
	if _, ok := names["Spam"]; ok {
		t.Error("denied folder Spam was listed")
	}
	if names["INBOX"].Total != 1 || names["INBOX"].Unread != 1 {
		t.Errorf("INBOX = %+v", names["INBOX"])
	}
}

func TestToolAnnotations(t *testing.T) {
	cs, _ := session(t, policy.Options{ReadOnly: true})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s: read-only mode advertises a tool without readOnlyHint", tool.Name)
		}
	}
}
