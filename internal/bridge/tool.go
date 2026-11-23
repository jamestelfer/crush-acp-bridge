package bridge

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/coder/acp-go-sdk"
)

// toolKind maps a Crush tool name to the ACP tool kind editors use for icons
// and grouping.
func toolKind(name string) acp.ToolKind {
	switch name {
	case "bash":
		return acp.ToolKindExecute
	case "edit", "multiedit", "write", "lsp_replace_symbol":
		return acp.ToolKindEdit
	case "view", "ls", "glob":
		return acp.ToolKindRead
	case "grep", "sourcegraph":
		return acp.ToolKindSearch
	case "fetch", "agentic_fetch", "download":
		return acp.ToolKindFetch
	default:
		return acp.ToolKindOther
	}
}

// toolArgs holds the arguments common to Crush's tools that are worth showing.
type toolArgs struct {
	Description string `json:"description"`
	Command     string `json:"command"`
	FilePath    string `json:"file_path"`
	Path        string `json:"path"`
	Pattern     string `json:"pattern"`
	URL         string `json:"url"`
	Query       string `json:"query"`
}

// describeTool returns a one-line title and the file locations for a tool
// call. Crush's own description wins; otherwise the title is the tool name
// followed by its main argument. Unparseable input yields the tool name.
func describeTool(name string, input jsontext.Value) (string, []acp.ToolCallLocation) {
	var a toolArgs
	if err := json.Unmarshal(input, &a); err != nil {
		return capitalise(name), nil
	}

	var locs []acp.ToolCallLocation
	if a.FilePath != "" {
		locs = []acp.ToolCallLocation{{Path: a.FilePath}}
	}

	if a.Description != "" {
		return a.Description, locs
	}
	for _, target := range []string{a.Command, a.FilePath, a.Path, a.Pattern, a.URL, a.Query} {
		if target != "" {
			return capitalise(name) + " " + target, locs
		}
	}
	return capitalise(name), locs
}

func capitalise(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + strings.ToLower(s[n:])
}
