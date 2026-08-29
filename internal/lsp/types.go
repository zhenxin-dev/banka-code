package lspclient

import (
	"encoding/json"
	"fmt"
)

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Position identifies a zero-based UTF-16 position in a document.
type Position = position

type lspRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

// Range is a zero-based UTF-16 document range.
type Range = lspRange

type location struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

// Location points at a range in a document.
type Location = location

type locationLink struct {
	TargetURI            string    `json:"targetUri"`
	TargetRange          lspRange  `json:"targetRange"`
	TargetSelectionRange *lspRange `json:"targetSelectionRange,omitempty"`
}

type diagnostic struct {
	Range    lspRange       `json:"range"`
	Severity int            `json:"severity,omitempty"`
	Code     any            `json:"code,omitempty"`
	Source   string         `json:"source,omitempty"`
	Message  string         `json:"message"`
	Data     map[string]any `json:"data,omitempty"`
}

// Diagnostic is a language-server diagnostic.
type Diagnostic = diagnostic

type textEdit struct {
	Range            lspRange `json:"range"`
	NewText          string   `json:"newText"`
	InsertTextFormat int      `json:"insertTextFormat,omitempty"`
}

// UnmarshalJSON validates the TextEdit shape that Banka can safely apply.
// InsertReplaceEdit carries different coordinates and must not be silently
// decoded as a zero range (which would corrupt the beginning of a file).
func (e *textEdit) UnmarshalJSON(data []byte) error {
	var raw struct {
		Range            *lspRange       `json:"range"`
		NewText          *string         `json:"newText"`
		InsertTextFormat int             `json:"insertTextFormat,omitempty"`
		Insert           json.RawMessage `json:"insert"`
		Replace          json.RawMessage `json:"replace"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.Insert) > 0 || len(raw.Replace) > 0 {
		return fmt.Errorf("InsertReplaceEdit is unsupported")
	}
	if raw.Range == nil || raw.NewText == nil {
		return fmt.Errorf("LSP text edit requires range and newText")
	}
	e.Range = *raw.Range
	e.NewText = *raw.NewText
	e.InsertTextFormat = raw.InsertTextFormat
	return nil
}

// TextEdit replaces a document range with NewText.
type TextEdit = textEdit

type workspaceEdit struct {
	Changes         map[string][]textEdit `json:"changes,omitempty"`
	DocumentChanges []json.RawMessage     `json:"documentChanges,omitempty"`
}

// WorkspaceEdit is the Language Server Protocol workspace edit payload.
type WorkspaceEdit = workspaceEdit

type command struct {
	Title     string `json:"title,omitempty"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// Command is an LSP command returned by a code action.
type Command = command

type codeAction struct {
	Title       string          `json:"title"`
	Kind        string          `json:"kind,omitempty"`
	Diagnostics []diagnostic    `json:"diagnostics,omitempty"`
	Edit        *workspaceEdit  `json:"edit,omitempty"`
	Command     *command        `json:"command,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
}

type documentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          lspRange         `json:"range"`
	SelectionRange lspRange         `json:"selectionRange"`
	Children       []documentSymbol `json:"children,omitempty"`
}

type symbolInformation struct {
	Name     string   `json:"name"`
	Kind     int      `json:"kind"`
	Location location `json:"location"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

type openDocument struct {
	URI      string
	Path     string
	Language string
	Version  int
	Content  string
}
