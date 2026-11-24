package bridge

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"path"
	"path/filepath"
	"strings"

	"github.com/coder/acp-go-sdk"
)

// Diffs for Crush's file editing tools. ACP diff content carries a file's
// text before and after a change; Paseo and Zed render it as a diff.
//
// Crush supplies whole files in two places: the parameters of a permission
// request (edit, multiedit and write), and the result metadata of edit and
// multiedit. Before either arrives, edit and multiedit show each replaced
// fragment. A write that Crush did not ask about records only a unified diff,
// which is shown as text.

// isEditTool reports whether name is a Crush tool that changes one file and
// describes the change.
func isEditTool(name string) bool {
	switch name {
	case "edit", "multiedit", "write":
		return true
	default:
		return false
	}
}

// editInput is the input of the edit, multiedit and write tools.
type editInput struct {
	FilePath  string `json:"file_path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
	Edits     []struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	} `json:"edits"`
	Content string `json:"content"`
}

// fileChange is a whole file before and after an edit, as Crush's permission
// parameters and edit result metadata record it. Crush omits empty content,
// so a missing old_content is a new or empty file.
type fileChange struct {
	FilePath   string  `json:"file_path"`
	OldContent *string `json:"old_content"`
	NewContent *string `json:"new_content"`
	Diff       string  `json:"diff"` // write results only
}

// inputDiffs returns diff content for an edit tool's input: each replaced
// fragment of an edit or multiedit. A write's earlier content is unknown, so
// it has none.
func inputDiffs(name, cwd string, input jsontext.Value) []acp.ToolCallContent {
	var in editInput
	if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
		return nil
	}
	p := absolute(cwd, in.FilePath)
	switch name {
	case "edit":
		return []acp.ToolCallContent{acp.ToolDiffContent(p, in.NewString, in.OldString)}
	case "multiedit":
		var out []acp.ToolCallContent
		for _, e := range in.Edits {
			out = append(out, acp.ToolDiffContent(p, e.NewString, e.OldString))
		}
		return out
	default:
		return nil
	}
}

// permissionDiffs returns diff content for the whole file an edit tool's
// permission request describes.
func permissionDiffs(name, cwd string, params jsontext.Value) []acp.ToolCallContent {
	if !isEditTool(name) {
		return nil
	}
	var c fileChange
	if json.Unmarshal(params, &c) != nil || c.FilePath == "" {
		return nil
	}
	return c.content(absolute(cwd, c.FilePath))
}

// resultDiffs returns the content for a finished edit tool call: the whole
// file before and after, or for a write Crush did not ask about, Crush's
// unified diff. It returns nil when nothing better than the tool's text output
// is known.
func resultDiffs(name, cwd string, input, params, metadata jsontext.Value) []acp.ToolCallContent {
	if !isEditTool(name) {
		return nil
	}
	var in editInput
	var asked, result fileChange
	_ = json.Unmarshal(input, &in)
	_ = json.Unmarshal(params, &asked)
	_ = json.Unmarshal(metadata, &result)
	filePath := cmp.Or(asked.FilePath, in.FilePath)
	if filePath == "" {
		return nil
	}
	p := absolute(cwd, filePath)

	if name != "write" {
		// The result records the edits that applied; the request, all of them.
		if d := result.content(p); d != nil {
			return d
		}
		return asked.content(p)
	}
	// A write result records only a unified diff.
	switch {
	case asked.OldContent != nil || asked.NewContent != nil:
		return asked.content(p)
	case createsFile(result.Diff):
		return []acp.ToolCallContent{acp.ToolDiffContent(p, in.Content)}
	case result.Diff != "":
		return []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(result.Diff))}
	default:
		return nil
	}
}

// content returns the change as ACP diff content for path, or nil when it
// records no content.
func (c fileChange) content(p string) []acp.ToolCallContent {
	if c.OldContent == nil && c.NewContent == nil {
		return nil
	}
	newText := ""
	if c.NewContent != nil {
		newText = *c.NewContent
	}
	if c.OldContent == nil {
		return []acp.ToolCallContent{acp.ToolDiffContent(p, newText)}
	}
	return []acp.ToolCallContent{acp.ToolDiffContent(p, newText, *c.OldContent)}
}

// createsFile reports whether a unified diff from Crush starts from an empty
// file: its only hunk adds lines at line 0.
func createsFile(unified string) bool {
	return strings.Contains(unified, "\n@@ -0,0 +") && strings.Count(unified, "\n@@ ") == 1
}

// absolute resolves a tool's file path against the session directory, as
// Crush does. Crush reports absolute paths in the server's own form, which
// may not be this platform's.
func absolute(cwd, p string) string {
	if filepath.IsAbs(p) || path.IsAbs(p) {
		return p
	}
	return filepath.Join(cwd, p)
}
