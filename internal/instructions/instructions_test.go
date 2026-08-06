package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrdersGlobalAndHierarchicalInstructions(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	nested := filepath.Join(root, "internal", "demo")
	mustWriteInstruction(t, filepath.Join(home, ".agents", "AGENTS.md"), "global")
	mustWriteInstruction(t, filepath.Join(root, ".git"), "gitdir: elsewhere")
	mustWriteInstruction(t, filepath.Join(root, "AGENTS.md"), "root")
	mustWriteInstruction(t, filepath.Join(root, "internal", "AGENTS.md"), "ignored")
	mustWriteInstruction(t, filepath.Join(root, "internal", "AGENTS.override.md"), "override")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	set, err := Load(nested, home)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if set.ProjectRoot != root || len(set.Documents) != 3 {
		t.Fatalf("unexpected instruction set: %#v", set)
	}
	rendered := set.Render()
	if strings.Index(rendered, "global") > strings.Index(rendered, "root") || strings.Contains(rendered, "ignored") || !strings.Contains(rendered, "override") {
		t.Fatalf("unexpected rendered instructions: %s", rendered)
	}
}

func mustWriteInstruction(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
