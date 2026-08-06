package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhenxin-dev/banka-code/internal/tools"
)

func TestDiscoverAppliesProjectSkillOverride(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	writeSkillFile(t, filepath.Join(home, ".agents", "skills", "demo", "SKILL.md"), "demo", "global")
	projectSkill := filepath.Join(project, ".agents", "skills", "demo", "SKILL.md")
	writeSkillFile(t, projectSkill, "demo", "project")

	catalog, err := Discover(project, home)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(catalog.Skills) != 1 || catalog.Skills[0].Path != projectSkill || !strings.Contains(catalog.Render(), "project") {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
}

func TestSkillToolRequiresMainFileBeforeResource(t *testing.T) {
	project := t.TempDir()
	skillPath := filepath.Join(project, ".agents", "skills", "demo", "SKILL.md")
	writeSkillFile(t, skillPath, "demo", "description")
	if err := os.WriteFile(filepath.Join(filepath.Dir(skillPath), "reference.md"), []byte("details"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := Discover(project, "")
	if err != nil {
		t.Fatal(err)
	}
	tool := NewTool(catalog)
	result, err := tool.Execute(context.Background(), map[string]any{"name": "demo", "resource": "reference.md"}, tools.Context{})
	if err != nil || !result.IsError {
		t.Fatalf("resource was available before main skill: result=%#v err=%v", result, err)
	}
	result, err = tool.Execute(context.Background(), map[string]any{"name": "demo"}, tools.Context{})
	if err != nil || !strings.Contains(result.Content, "# demo") {
		t.Fatalf("failed to load main skill: result=%#v err=%v", result, err)
	}
	result, err = tool.Execute(context.Background(), map[string]any{"name": "demo", "resource": "reference.md"}, tools.Context{})
	if err != nil || result.Content != "details" {
		t.Fatalf("failed to load resource: result=%#v err=%v", result, err)
	}
}

func writeSkillFile(t *testing.T, path string, name string, description string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
