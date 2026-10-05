//go:build integration

// Run against a real Proton Mail Bridge with:
//
//	PROTON_USER=... PROTON_BRIDGE_PASSWORD=... BRIDGE_CERT=... go test -tags integration ./internal/tools -run Integration -v
//
// Read checks only, unless PROTONMCP_IT_DRAFTS=1, which also creates, updates
// and removes one draft addressed to yourself.
package tools_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/henrrrik/proton-mail-mcp/internal/config"
	"github.com/henrrrik/proton-mail-mcp/internal/mail"
	"github.com/henrrrik/proton-mail-mcp/internal/policy"
	"github.com/henrrrik/proton-mail-mcp/internal/tools"
)

func TestIntegrationBridge(t *testing.T) {
	cfg, err := config.Load(os.Getenv("PROTONMCP_CONFIG"), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := mail.TLSConfig(cfg.BridgeCert, mail.HostOf(cfg.IMAPAddr), cfg.InsecureLoopback)
	if err != nil {
		t.Fatal(err)
	}
	mc := mail.New(mail.Options{Addr: cfg.IMAPAddr, User: cfg.User, Password: cfg.Password, TLS: tlsCfg})
	defer mc.Close()
	drafts := os.Getenv("PROTONMCP_IT_DRAFTS") == "1"
	gate, _ := policy.New(policy.Options{ReadOnly: !drafts})

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := tools.NewServer("it", tools.Deps{Gate: gate, Mail: mc, Self: cfg.User}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "it", Version: "it"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	h := &harness{t: t, cs: cs}

	if folders := h.call("list_folders", nil, false); !strings.Contains(folders, "INBOX") {
		t.Fatalf("list_folders: %s", folders)
	}
	inbox := ids(h.call("list_messages", map[string]any{"folder": "INBOX", "limit": 3}, false))
	t.Logf("INBOX ids: %v", inbox)
	if len(inbox) > 0 {
		h.call("get_message", map[string]any{"id": inbox[0], "max_chars": 2000}, false)
		h.call("get_thread", map[string]any{"id": inbox[0]}, false)
	}
	t.Log(h.call("search", map[string]any{"subject": "ä", "limit": 3}, false))
	if !drafts {
		return
	}

	var r draftResult
	h.jsonCall("create_draft", map[string]any{"to": []string{cfg.User}, "subject": "protonmcp integration test", "body": "Safe to delete."}, &r)
	h.jsonCall("update_draft", map[string]any{"id": r.DraftID, "body": "Safe to delete (updated)."}, &r)
	if r.Warning != "" {
		t.Error(r.Warning)
	}
	id, _ := mail.ParseID(r.DraftID)
	if err := mc.DeleteDraft(id); err != nil {
		t.Errorf("cleanup: %v", err)
	}
}
