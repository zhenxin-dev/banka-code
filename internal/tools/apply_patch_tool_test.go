package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchValidatesThenUpdatesWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	path := filepath.Join(root, "demo.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "--- a/demo.txt\n+++ b/demo.txt\n@@ -1 +1 @@\n-old\n+new\n"
	result, err := NewApplyPatchTool().Execute(context.Background(), map[string]any{"patch": patch}, Context{WorkspaceRoot: root})
	if err != nil || result.IsError {
		t.Fatalf("ApplyPatch failed: result=%#v err=%v", result, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "new\n" {
		t.Fatalf("unexpected patched file: %q err=%v", content, err)
	}
}

func TestApplyPatchRejectsEscapingPath(t *testing.T) {
	patch := "--- a/../outside.txt\n+++ b/../outside.txt\n@@ -1 +1 @@\n-old\n+new\n"
	_, err := NewApplyPatchTool().Execute(context.Background(), map[string]any{"patch": patch}, Context{WorkspaceRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("got error %v, want workspace escape", err)
	}
}

func TestApplyPatchSupportsPathsWithSpaces(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	path := filepath.Join(root, "file with space.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "--- a/file with space.txt\n+++ b/file with space.txt\n@@ -1 +1 @@\n-old\n+new\n"
	result, err := NewApplyPatchTool().Execute(context.Background(), map[string]any{"patch": patch}, Context{WorkspaceRoot: root})
	if err != nil || result.IsError {
		t.Fatalf("ApplyPatch failed: result=%#v err=%v", result, err)
	}
}

func TestPatchTargetPathsClassifiesOneLogicalModification(t *testing.T) {
	targets := patchTargetPaths("--- a/demo.txt\n+++ b/demo.txt\n@@ -1 +1 @@\n-old\n+new\n")
	if len(targets) != 1 || targets[0].path != "demo.txt" || targets[0].operation != "patch" {
		t.Fatalf("unexpected modification targets: %#v", targets)
	}
}

func TestPatchTargetPathsClassifiesCreateAndDelete(t *testing.T) {
	created := patchTargetPaths("--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n")
	if len(created) != 1 || created[0].path != "new.txt" || created[0].operation != "create" {
		t.Fatalf("unexpected create target: %#v", created)
	}
	deleted := patchTargetPaths("--- a/old.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n")
	if len(deleted) != 1 || deleted[0].path != "old.txt" || deleted[0].operation != "delete" {
		t.Fatalf("unexpected delete target: %#v", deleted)
	}
}
