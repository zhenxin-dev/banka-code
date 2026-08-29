package lspclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSwiftLintDiagnosticsConvertsCoordinatesAndFiltersFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "Demo.swift")
	content := "😀 let value = 1\n"
	output := `[{"file":"Demo.swift","line":1,"character":3,"reason":"rename this","rule_id":"identifier_name","severity":"Warning"},{"file":"other.swift","line":1,"character":1,"reason":"other","rule_id":"x","severity":"Error"}]`
	values, err := parseSwiftLintDiagnostics(output, target, content, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %#v", len(values), values)
	}
	// SwiftLint columns are 1-based scalar columns; the emoji occupies two
	// UTF-16 code units before the third scalar column.
	if values[0].Range.Start.Line != 0 || values[0].Range.Start.Character != 3 || values[0].Severity != 2 {
		t.Fatalf("unexpected converted diagnostic: %#v", values[0])
	}
}

func TestParseBiomeDiagnosticsConvertsCoordinatesAndFiltersFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "src", "index.ts")
	content := "😀 x\n"
	output := `{"diagnostics":[{"category":"lint/correctness/noUnusedVariables","severity":"error","message":"unused","location":{"path":"src/index.ts","start":{"line":1,"column":3},"end":{"line":1,"column":4}}},{"category":"lint/x","severity":"warning","message":"outside","location":{"path":"src/other.ts","start":{"line":1,"column":1}}}]}`
	values, err := parseBiomeDiagnostics(output, target, content, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Severity != 1 || values[0].Range.Start.Character != 3 {
		t.Fatalf("unexpected Biome diagnostics: %#v", values)
	}
}

func TestParseBiomeDiagnosticsAcceptsStructuredMessages(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "index.ts")
	tests := []struct {
		name        string
		message     string
		description string
		want        string
	}{
		{name: "string", message: `"plain"`, want: "plain"},
		{name: "object", message: `{"content":"object message"}`, want: "object message"},
		{name: "array", message: `[{"content":"first"},{"text":"second"}]`, want: "first second"},
		{name: "description fallback", message: `null`, description: `,"description":{"value":"fallback"}`, want: "fallback"},
		{name: "unknown object", message: `{"future":{"detail":"value"}}`, want: `{"future":{"detail":"value"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := `{"diagnostics":[{"category":"lint/demo","severity":"warning","message":` + test.message + test.description + `,"location":{"path":"index.ts","start":{"line":1,"column":1}}}]}`
			values, err := parseBiomeDiagnostics(output, target, "let value = 1\n", root, root)
			if err != nil {
				t.Fatal(err)
			}
			if len(values) != 1 || values[0].Message != test.want {
				t.Fatalf("unexpected message: %#v, want %q", values, test.want)
			}
		})
	}
}

func TestReportedFileMatchesFileURIWithinWorkspace(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "with space", "世界.ts")
	for _, reported := range []string{
		fileURI(target),
		"FILE://LOCALHOST" + urlPathEscape(filepath.ToSlash(target)),
	} {
		if !reportedFileMatches(root, target, reported, root) {
			t.Errorf("workspace file URI did not match target: %s", reported)
		}
	}

	outside := filepath.Join(filepath.Dir(root), "outside.ts")
	if reportedFileMatches(root, target, fileURI(outside), root) {
		t.Fatal("outside file URI matched a workspace target")
	}
}

func TestLinterDiagnosticRunnerAcceptsViolationExitStatus(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.swift")
	if err := os.WriteFile(path, []byte("let x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "swiftlint")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf '%s' '[{\"file\":\"demo.swift\",\"line\":1,\"character\":1,\"reason\":\"demo violation\",\"rule_id\":\"demo\",\"severity\":\"Error\"}]'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := ServerConfig{Command: command, ResolvedCommand: command, Linter: "swiftlint"}
	manager := NewManager(root, "test", Config{Servers: map[string]ServerConfig{"swift": server}})
	defer manager.Close()
	values, err := manager.LinterDiagnostics(context.Background(), "swift", server, path, "let x = 1\n")
	if err != nil || len(values) != 1 || values[0].Source != "swiftlint" {
		t.Fatalf("unexpected linter result: values=%#v err=%v", values, err)
	}
}
