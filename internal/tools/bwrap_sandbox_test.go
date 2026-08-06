package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBubblewrapAvailabilityIsStable(t *testing.T) {
	first := isBubblewrapAvailable()
	second := isBubblewrapAvailable()
	if first != second {
		t.Fatalf("availability changed from %v to %v", first, second)
	}
}

func TestDirectShellCommandCapturesOutput(t *testing.T) {
	root := t.TempDir()
	shell := "zsh"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "bash"
	}
	cmd := exec.CommandContext(context.Background(), shell, "-lc", "echo hello")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("direct shell command failed: %v", err)
	}
	if strings.TrimSpace(string(output)) != "hello" {
		t.Fatalf("got output %q, want hello", output)
	}
}

func TestSandboxedShellCanReadWorkspaceButNotOutsideFile(t *testing.T) {
	if !isBubblewrapAvailable() {
		t.Skip("bubblewrap is unavailable")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inside := exec.CommandContext(context.Background(), "bwrap", buildBwrapArgs("cat inside.txt", root)...)
	inside.Dir = root
	output, err := inside.Output()
	if err != nil {
		t.Fatalf("sandbox could not read workspace file: %v", err)
	}
	if string(output) != "inside\n" {
		t.Fatalf("got output %q, want inside", output)
	}

	outsideCommand := exec.CommandContext(context.Background(), "bwrap", buildBwrapArgs("cat "+secret, root)...)
	outsideCommand.Dir = root
	var stderr bytes.Buffer
	outsideCommand.Stderr = &stderr
	if err := outsideCommand.Run(); err == nil {
		t.Fatalf("sandbox unexpectedly read outside file")
	}
	if stderr.Len() == 0 {
		t.Fatalf("sandbox failure did not report stderr")
	}
}

func TestBashToolUsesSandboxAndFormatsResult(t *testing.T) {
	root := t.TempDir()
	result, err := NewBashTool().Execute(context.Background(), map[string]any{"command": "printf banka"}, Context{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Bash returned error: %v", err)
	}
	if result.IsError || !strings.Contains(result.Content, "exit_code: 0") || !strings.Contains(result.Content, "stdout:\nbanka") {
		t.Fatalf("unexpected Bash result: %#v", result)
	}
}

func TestChildEnvironmentExcludesBankaCredentials(t *testing.T) {
	t.Setenv("BANKA_API_KEY", "must-not-leak")
	t.Setenv("BANKA_MODEL", "must-not-leak")
	t.Setenv("VISIBLE_TEST_VALUE", "visible")
	environment := strings.Join(sanitizedChildEnvironment(nil), "\n")
	if strings.Contains(environment, "BANKA_") || !strings.Contains(environment, "VISIBLE_TEST_VALUE=visible") {
		t.Fatalf("unexpected child environment: %s", environment)
	}
	for _, pair := range collectMinimalEnvironment() {
		if strings.HasPrefix(pair[0], "BANKA_") {
			t.Fatalf("sandbox inherited credential variable %s", pair[0])
		}
	}
}
