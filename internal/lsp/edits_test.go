package lspclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyWorkspaceEditUsesUTF16Positions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.txt")
	if err := os.WriteFile(path, []byte("😀x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := workspaceEdit{Changes: map[string][]textEdit{
		fileURI(path): {{Range: lspRange{Start: position{Line: 0, Character: 2}, End: position{Line: 0, Character: 3}}, NewText: "y"}},
	}}
	if err := applyWorkspaceEdit(context.Background(), root, edit, nil); err != nil {
		t.Fatalf("applyWorkspaceEdit returned error: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "😀y\n" {
		t.Fatalf("got %q, want UTF-16-aware edit", content)
	}
}

func TestApplyWorkspaceEditRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := workspaceEdit{Changes: map[string][]textEdit{
		fileURI(outside): {{Range: lspRange{Start: position{}, End: position{Line: 0, Character: 3}}, NewText: "new"}},
	}}
	if err := applyWorkspaceEdit(context.Background(), root, edit, nil); err == nil {
		t.Fatal("workspace escape was accepted")
	}
}

func TestFileURIRoundTripPreservesEscapedPercent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space 世界 literal%2F.txt")
	got, err := uriToPath(fileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("URI round trip = %q, want %q", got, path)
	}

	localhostURI := "FILE://LOCALHOST" + urlPathEscape(filepath.ToSlash(path))
	got, err = uriToPath(localhostURI)
	if err != nil || got != path {
		t.Fatalf("localhost URI = %q, err=%v, want %q", got, err, path)
	}
}

func TestDecodeLocationsDistinguishesLocationLinks(t *testing.T) {
	raw := []byte(`[{"targetUri":"file:///workspace/target.go","targetRange":{"start":{"line":2,"character":1},"end":{"line":2,"character":4}},"targetSelectionRange":{"start":{"line":2,"character":1},"end":{"line":2,"character":2}}}]`)
	locations, err := decodeLocations(raw)
	if err != nil {
		t.Fatalf("decodeLocations returned error: %v", err)
	}
	if len(locations) != 1 || locations[0].URI != "file:///workspace/target.go" || locations[0].Range.End.Character != 2 {
		t.Fatalf("unexpected location-link conversion: %#v", locations)
	}
}

func TestDecodeLocationsRejectsMalformedLocation(t *testing.T) {
	if _, err := decodeLocations([]byte(`[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}]`)); err == nil {
		t.Fatal("malformed location was accepted")
	}
}

func TestApplyWorkspaceEditHonorsResourceOperationOptions(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	destination := filepath.Join(root, "nested", "destination.txt")
	if err := os.WriteFile(source, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := workspaceEdit{DocumentChanges: []json.RawMessage{
		mustJSON(`{"kind":"rename","oldUri":"` + fileURI(source) + `","newUri":"` + fileURI(destination) + `"}`),
		mustJSON(`{"kind":"create","uri":"` + fileURI(filepath.Join(root, "existing.txt")) + `","options":{"ignoreIfExists":true}}`),
	}}
	if err := applyWorkspaceEdit(context.Background(), root, edit, nil); err != nil {
		t.Fatalf("applyWorkspaceEdit returned error: %v", err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists after rename: %v", err)
	}
	content, err := os.ReadFile(destination)
	if err != nil || string(content) != "hello" {
		t.Fatalf("destination content mismatch: %q err=%v", content, err)
	}
}

func TestApplyWorkspaceEditSupportsCreateThenTextEdit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "new.txt")
	edit := workspaceEdit{DocumentChanges: []json.RawMessage{
		mustJSON(`{"kind":"create","uri":"` + fileURI(path) + `"}`),
		mustJSON(`{"textDocument":{"uri":"` + fileURI(path) + `"},"edits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}},"newText":"created"}]}`),
	}}
	if err := applyWorkspaceEdit(context.Background(), root, edit, nil); err != nil {
		t.Fatalf("create/text edit failed: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "created" {
		t.Fatalf("unexpected created content: %q err=%v", content, err)
	}
}

func TestApplyWorkspaceEditSupportsOrderedRenameThenTextEdit(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "old.txt")
	destination := filepath.Join(root, "new.txt")
	if err := os.WriteFile(source, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := workspaceEdit{DocumentChanges: []json.RawMessage{
		mustJSON(`{"kind":"rename","oldUri":"` + fileURI(source) + `","newUri":"` + fileURI(destination) + `"}`),
		mustJSON(`{"textDocument":{"uri":"` + fileURI(destination) + `"},"edits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":6}},"newText":"after"}]}`),
	}}
	if err := applyWorkspaceEdit(context.Background(), root, edit, nil); err != nil {
		t.Fatalf("ordered rename/text edit failed: %v", err)
	}
	content, err := os.ReadFile(destination)
	if err != nil || string(content) != "after" {
		t.Fatalf("unexpected destination content: %q err=%v", content, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
}

func TestApplyWorkspaceEditSupportsOrderedDirectoryRenameThenChildEdit(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "old")
	destination := filepath.Join(root, "new")
	child := filepath.Join(source, "nested", "demo.txt")
	if err := os.MkdirAll(filepath.Dir(child), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	newChild := filepath.Join(destination, "nested", "demo.txt")
	edit := workspaceEdit{DocumentChanges: []json.RawMessage{
		mustJSON(`{"kind":"rename","oldUri":"` + fileURI(source) + `","newUri":"` + fileURI(destination) + `"}`),
		mustJSON(`{"textDocument":{"uri":"` + fileURI(newChild) + `"},"edits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":6}},"newText":"after"}]}`),
	}}
	if err := applyWorkspaceEdit(context.Background(), root, edit, nil); err != nil {
		t.Fatalf("ordered directory rename/text edit failed: %v", err)
	}
	content, err := os.ReadFile(newChild)
	if err != nil || string(content) != "after" {
		t.Fatalf("unexpected child content: %q err=%v", content, err)
	}
}

func TestApplyWorkspaceEditPreservesTextBeforeUnrelatedResourceOperation(t *testing.T) {
	root := t.TempDir()
	textPath := filepath.Join(root, "old.txt")
	otherPath := filepath.Join(root, "other.txt")
	if err := os.WriteFile(textPath, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The text edit must run while old.txt still exists; the following rename
	// touches a different path but is still ordered after it by the protocol.
	edit := workspaceEdit{DocumentChanges: []json.RawMessage{
		mustJSON(`{"textDocument":{"uri":"` + fileURI(textPath) + `"},"edits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":6}},"newText":"changed"}]}`),
		mustJSON(`{"kind":"rename","oldUri":"` + fileURI(otherPath) + `","newUri":"` + fileURI(filepath.Join(root, "renamed.txt")) + `"}`),
	}}
	if err := applyWorkspaceEdit(context.Background(), root, edit, nil); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(textPath)
	if err != nil || string(content) != "changed" {
		t.Fatalf("text edit was reordered past resource operation: %q err=%v", content, err)
	}
}

func TestApplyTextEditsPreservesInsertionOrder(t *testing.T) {
	content, err := applyTextEdits([]byte("x"), []textEdit{
		{Range: lspRange{Start: position{}, End: position{}}, NewText: "A"},
		{Range: lspRange{Start: position{}, End: position{}}, NewText: "B"},
	})
	if err != nil || string(content) != "ABx" {
		t.Fatalf("unexpected insertion result: %q err=%v", content, err)
	}
}

func TestApplyWorkspaceEditRejectsSnippetEdit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "snippet.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := workspaceEdit{Changes: map[string][]textEdit{fileURI(path): {{
		Range: lspRange{Start: position{}, End: position{}}, NewText: "${1:x}", InsertTextFormat: 2,
	}}}}
	if err := applyWorkspaceEdit(context.Background(), root, edit, nil); err == nil {
		t.Fatal("snippet edit was accepted")
	}
}

func mustJSON(value string) json.RawMessage {
	return json.RawMessage(value)
}
