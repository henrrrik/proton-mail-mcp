package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/henrrrik/proton-mail-mcp/internal/mail"
	"github.com/henrrrik/proton-mail-mcp/internal/policy"
)

type listFoldersOut struct {
	Folders []mail.Folder `json:"folders"`
}

func addListFolders(s *mcp.Server, d Deps) {
	add(s, d.Gate, policy.Read, &mcp.Tool{
		Name:        "list_folders",
		Description: "List mail folders and labels with total and unread counts. Labels have names starting with Labels/, user folders with Folders/.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listFoldersOut, error) {
		folders, err := d.Mail.ListFolders(d.Gate.FolderAllowed)
		if folders == nil {
			folders = []mail.Folder{}
		}
		return nil, listFoldersOut{Folders: folders}, err
	})
}
