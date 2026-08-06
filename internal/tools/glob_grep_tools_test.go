package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobToolFindsNestedFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "cmd", "banka", "main.go"), "package main")
	mustWrite(t, filepath.Join(root, "README.md"), "# Banka")

	result, err := NewGlobTool().Execute(context.Background(), map[string]any{"pattern": "**/*.go"}, Context{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if result.Content != "cmd/banka/main.go" {
		t.Fatalf("got %q, want %q", result.Content, "cmd/banka/main.go")
	}
}

func TestGlobDoubleStarMatchesZeroOrMoreDirectories(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "app.go"), "package app")
	mustWrite(t, filepath.Join(root, "src", "nested", "more.go"), "package nested")

	result, err := NewGlobTool().Execute(context.Background(), map[string]any{"pattern": "src/**/*.go"}, Context{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if !strings.Contains(result.Content, "src/app.go") || !strings.Contains(result.Content, "src/nested/more.go") {
		t.Fatalf("double-star did not match zero and multiple directories: %q", result.Content)
	}
}

func TestGlobRejectsInvalidOptionalPath(t *testing.T) {
	_, err := NewGlobTool().Execute(context.Background(), map[string]any{"pattern": "**/*", "path": ""}, Context{WorkspaceRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "non-empty string") {
		t.Fatalf("got error %v, want invalid path error", err)
	}
}

func TestGrepToolSearchesContent(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "hello\nbanka\n")
	mustWrite(t, filepath.Join(root, "b.md"), "banka\n")

	result, err := NewGrepTool().Execute(context.Background(), map[string]any{
		"pattern": "banka",
		"include": "*.txt",
	}, Context{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Grep returned error: %v", err)
	}

	if !strings.Contains(result.Content, "a.txt:2: banka") {
		t.Fatalf("unexpected grep output: %q", result.Content)
	}
	if strings.Contains(result.Content, "b.md") {
		t.Fatalf("include filter was not applied: %q", result.Content)
	}
}

func TestGrepRejectsInvalidOptionalArguments(t *testing.T) {
	tests := []map[string]any{
		{"pattern": "x", "path": ""},
		{"pattern": "x", "include": 42},
		{"pattern": "x", "outputMode": "unknown"},
	}
	for _, arguments := range tests {
		if _, err := NewGrepTool().Execute(context.Background(), arguments, Context{WorkspaceRoot: t.TempDir()}); err == nil {
			t.Fatalf("Grep accepted invalid arguments: %#v", arguments)
		}
	}
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
}
