package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSafePathAllowsWorkspacePath(t *testing.T) {
	root := t.TempDir()

	got, err := ResolveSafePath(root, "cmd/banka/main.go")
	if err != nil {
		t.Fatalf("ResolveSafePath returned error: %v", err)
	}

	want := filepath.Join(root, "cmd", "banka", "main.go")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveSafePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	for _, target := range []string{"outside-link", "outside-link/new.txt"} {
		if _, err := ResolveSafePath(root, target); err == nil {
			t.Fatalf("expected symlink escape %q to be rejected", target)
		}
	}
}

func TestResolveSafePathAllowsSymlinkInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "inside-link")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := ResolveSafePath(root, "inside-link/new.txt"); err != nil {
		t.Fatalf("inside symlink was rejected: %v", err)
	}
}

func TestResolveSafePathRejectsEscapingPath(t *testing.T) {
	root := t.TempDir()

	_, err := ResolveSafePath(root, "../outside.txt")
	if err == nil {
		t.Fatalf("expected escaping path to be rejected")
	}
}
