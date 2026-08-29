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

func TestDiscoverUsesCompatibleSkillRoots(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	writeSkillFile(t, filepath.Join(home, ".banka", "skills", "banka-only", "SKILL.md"), "banka-only", "banka")
	writeSkillFile(t, filepath.Join(home, ".agents", "skills", "shared", "SKILL.md"), "shared", "global")
	projectSkill := filepath.Join(project, ".banka", "skills", "shared", "SKILL.md")
	writeSkillFile(t, projectSkill, "shared", "project")
	writeSkillFile(t, filepath.Join(project, ".codex", "skills", "codex", "SKILL.md"), "codex", "compatible")

	catalog, err := Discover(project, home)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(catalog.Skills) != 3 {
		t.Fatalf("got %d skills, want 3: %#v", len(catalog.Skills), catalog.Skills)
	}
	if catalog.Skills[0].Name != "banka-only" || catalog.Skills[1].Name != "codex" || catalog.Skills[2].Name != "shared" || catalog.Skills[2].Path != projectSkill {
		t.Fatalf("unexpected catalog: %#v", catalog.Skills)
	}
}

func TestDiscoverSupportsPiAgentAndGitHubSkillRoots(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	writeSkillFile(t, filepath.Join(home, ".pi", "agent", "skills", "pi-user", "SKILL.md"), "pi-user", "pi")
	writeSkillFile(t, filepath.Join(project, ".github", "skills", "github", "SKILL.md"), "github", "github")
	writeSkillFile(t, filepath.Join(project, ".pi", "agent", "skills", "pi-project", "SKILL.md"), "pi-project", "project")
	catalog, err := Discover(project, home)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, skill := range catalog.Skills {
		seen[skill.Name] = true
	}
	if !seen["pi-user"] || !seen["pi-project"] || !seen["github"] {
		t.Fatalf("compatible skill roots were not discovered: %#v", catalog.Skills)
	}
}

func TestSkillToolRejectsSymlinkedResourceOutsideDirectory(t *testing.T) {
	project := t.TempDir()
	skillPath := filepath.Join(project, ".agents", "skills", "demo", "SKILL.md")
	writeSkillFile(t, skillPath, "demo", "description")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(filepath.Dir(skillPath), "secret.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	catalog, err := Discover(project, "")
	if err != nil {
		t.Fatal(err)
	}
	tool := NewTool(catalog)
	_, _ = tool.Execute(context.Background(), map[string]any{"name": "demo"}, tools.Context{})
	result, err := tool.Execute(context.Background(), map[string]any{"name": "demo", "resource": "secret.txt"}, tools.Context{})
	if err == nil && !result.IsError {
		t.Fatalf("outside symlink resource was accepted: result=%#v err=%v", result, err)
	}
}

func TestReadMetadataSupportsMultilineDescriptionAndHiddenSkills(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".agents", "skills", "hidden", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: hidden\ndescription: >\n  Review changed files\n  and run focused tests.\nhide: true\ndisable-model-invocation: false\n---\n# Hidden\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog, err := Discover(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 1 || !catalog.Skills[0].Hidden || catalog.Skills[0].Description != "Review changed files and run focused tests." {
		t.Fatalf("unexpected hidden skill metadata: %#v", catalog)
	}
	if rendered := catalog.Render(); rendered != "" {
		t.Fatalf("hidden skill leaked into prompt: %q", rendered)
	}
	if result, err := NewTool(catalog).Execute(context.Background(), map[string]any{"name": "hidden"}, tools.Context{}); err != nil || !strings.Contains(result.Content, "# Hidden") {
		t.Fatalf("hidden skill was not explicitly loadable: result=%#v err=%v", result, err)
	}
}

func TestDiscoverSkipsInvalidSkillWithWarning(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".agents", "skills", "broken", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog, err := Discover(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 0 || len(catalog.Warnings) != 1 || !strings.Contains(catalog.Warnings[0], "missing closing") {
		t.Fatalf("unexpected invalid-skill result: %#v", catalog)
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

func TestSkillToolAcceptsResourceAfterInteractiveMainLoad(t *testing.T) {
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
	if _, _, err := catalog.Load("demo"); err != nil {
		t.Fatal(err)
	}
	if err := tool.MarkLoaded("demo"); err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), map[string]any{"uri": "skill://demo/reference.md"}, tools.Context{})
	if err != nil || result.Content != "details" {
		t.Fatalf("interactive skill load did not unlock resources: result=%#v err=%v", result, err)
	}
}

func TestSkillToolSupportsSkillURI(t *testing.T) {
	project := t.TempDir()
	skillPath := filepath.Join(project, ".agents", "skills", "demo", "SKILL.md")
	writeSkillFile(t, skillPath, "demo", "description")
	resourcePath := filepath.Join(filepath.Dir(skillPath), "references", "guide.md")
	if err := os.MkdirAll(filepath.Dir(resourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resourcePath, []byte("guide"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := Discover(project, "")
	if err != nil {
		t.Fatal(err)
	}
	tool := NewTool(catalog)
	main, err := tool.Execute(context.Background(), map[string]any{"uri": "skill://demo"}, tools.Context{})
	if err != nil || !strings.Contains(main.Content, "# demo") {
		t.Fatalf("skill URI main load failed: result=%#v err=%v", main, err)
	}
	result, err := tool.Execute(context.Background(), map[string]any{"uri": "skill://demo/references/guide.md"}, tools.Context{})
	if err != nil || result.Content != "guide" {
		t.Fatalf("skill URI resource load failed: result=%#v err=%v", result, err)
	}
	if result, err := tool.Execute(context.Background(), map[string]any{"uri": "skill://demo/../secret"}, tools.Context{}); err == nil && !result.IsError {
		t.Fatal("skill URI traversal was accepted")
	}
	if result, err := tool.Execute(context.Background(), map[string]any{"uri": "skill://demo/%252e%252e/secret"}, tools.Context{}); err == nil && !result.IsError {
		t.Fatal("double-encoded skill URI traversal was accepted")
	}
}

func TestCatalogLoadStripsFrontmatterForSlashInvocation(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".agents", "skills", "demo", "SKILL.md")
	writeSkillFile(t, path, "demo", "description")
	catalog, err := Discover(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if names := catalog.Names(); len(names) != 1 || names[0] != "demo" {
		t.Fatalf("unexpected skill names: %#v", names)
	}
	skill, content, err := catalog.Load("demo")
	if err != nil || skill.Name != "demo" || strings.Contains(content, "description: description") || !strings.Contains(content, "# demo") {
		t.Fatalf("catalog load did not strip frontmatter: skill=%#v content=%q err=%v", skill, content, err)
	}
}

func TestAlwaysApplyContentLoadsTrustedVisibleSkillsWithBound(t *testing.T) {
	project := t.TempDir()
	visible := filepath.Join(project, ".agents", "skills", "always", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(visible), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(visible, []byte("---\nname: always\nalwaysApply: true\n---\n# Always\nkeep this rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(project, ".agents", "skills", "hidden-always", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(hidden), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hidden, []byte("---\nname: hidden-always\nalwaysApply: true\nhide: true\n---\nsecret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := Discover(project, "")
	if err != nil {
		t.Fatal(err)
	}
	content := catalog.AlwaysApplyContent()
	if !strings.Contains(content, "keep this rule") || strings.Contains(content, "secret") || strings.Contains(content, "alwaysApply: true") {
		t.Fatalf("unexpected always-apply content: %q", content)
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
