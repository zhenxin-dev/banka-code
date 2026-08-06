package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReadAndEditTools(t *testing.T) {
	root := t.TempDir()
	toolContext := Context{WorkspaceRoot: root}

	writeResult, err := NewWriteTool().Execute(context.Background(), map[string]any{
		"path":    "notes/demo.txt",
		"content": "hello banka",
	}, toolContext)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if writeResult.IsError {
		t.Fatalf("Write returned error result: %s", writeResult.Content)
	}

	readResult, err := NewReadTool().Execute(context.Background(), map[string]any{"path": "notes/demo.txt"}, toolContext)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if readResult.Content != "hello banka" {
		t.Fatalf("got %q, want %q", readResult.Content, "hello banka")
	}

	editResult, err := NewEditTool().Execute(context.Background(), map[string]any{
		"path":    "notes/demo.txt",
		"oldText": "banka",
		"newText": "go",
	}, toolContext)
	if err != nil {
		t.Fatalf("Edit returned error: %v", err)
	}
	if editResult.IsError {
		t.Fatalf("Edit returned error result: %s", editResult.Content)
	}

	content, err := os.ReadFile(filepath.Join(root, "notes", "demo.txt"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(content) != "hello go" {
		t.Fatalf("got %q, want %q", string(content), "hello go")
	}
}

func TestEditRejectsNonUniqueTarget(t *testing.T) {
	root := t.TempDir()
	toolContext := Context{WorkspaceRoot: root}
	path := filepath.Join(root, "demo.txt")
	if err := os.WriteFile(path, []byte("same same"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	_, err := NewEditTool().Execute(context.Background(), map[string]any{
		"path":    "demo.txt",
		"oldText": "same",
		"newText": "next",
	}, toolContext)
	if err == nil {
		t.Fatalf("expected non-unique target to be rejected")
	}
}

func TestEditAllowsWhitespaceTargetAndLiteralDollarReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewEditTool().Execute(context.Background(), map[string]any{
		"path": "demo.txt", "oldText": " ", "newText": "$&-literal",
	}, Context{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Edit returned error: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello$&-literalworld\n" {
		t.Fatalf("got %q, want literal replacement", content)
	}
}

func TestReadSupportsLineOffsetAndLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lines.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := NewReadTool().Execute(context.Background(), map[string]any{
		"path": "lines.txt", "offset": float64(2), "limit": float64(2),
	}, Context{WorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Content, "two\nthree\n") || !strings.Contains(result.Content, "lines 2-3 of 4") {
		t.Fatalf("unexpected partial read: %q", result.Content)
	}
}
