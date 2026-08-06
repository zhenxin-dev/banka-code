package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/zhenxin-dev/banka-code/internal/tools"
)

const maxSkillResourceBytes = 1_000_000

type skillTool struct {
	byName map[string]Skill
	mu     sync.Mutex
	loaded map[string]bool
}

// NewTool creates the on-demand Skill loader.
func NewTool(catalog Catalog) tools.Definition {
	byName := make(map[string]Skill, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		byName[skill.Name] = skill
	}
	return &skillTool{byName: byName, loaded: make(map[string]bool)}
}

func (*skillTool) Name() string { return "Skill" }
func (*skillTool) Description() string {
	return "Load a discovered SKILL.md completely, or load one of its referenced resources after the main skill has been loaded."
}
func (t *skillTool) InputSchema() tools.JSONSchema {
	names := make([]string, 0, len(t.byName))
	for name := range t.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return tools.JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type": "string", "description": "Discovered skill name.", "enum": names,
			},
			"resource": map[string]any{
				"type": "string", "description": "Optional path relative to the skill directory. Load SKILL.md first.",
			},
		},
		"required": []string{"name"}, "additionalProperties": false,
	}
}
func (t *skillTool) Execute(_ context.Context, arguments map[string]any, _ tools.Context) (tools.Result, error) {
	name, ok := arguments["name"].(string)
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return tools.Result{}, errors.New("Skill tool requires a non-empty 'name' string")
	}
	skill, ok := t.byName[name]
	if !ok {
		return tools.Result{}, fmt.Errorf("unknown skill: %s", name)
	}
	resource, hasResource := arguments["resource"]
	if !hasResource || strings.TrimSpace(fmt.Sprint(resource)) == "" {
		content, err := readSkillFile(skill.Path, skill.Directory)
		if err != nil {
			return tools.Result{}, err
		}
		t.mu.Lock()
		t.loaded[name] = true
		t.mu.Unlock()
		return tools.Result{Content: fmt.Sprintf("Skill directory: %s\n\n%s", skill.Directory, content)}, nil
	}
	resourcePath, ok := resource.(string)
	if !ok || strings.TrimSpace(resourcePath) == "" {
		return tools.Result{}, errors.New("Skill tool requires 'resource' to be a non-empty string when provided")
	}
	t.mu.Lock()
	loaded := t.loaded[name]
	t.mu.Unlock()
	if !loaded {
		return tools.Result{Content: "Load the skill's SKILL.md before requesting its resources.", IsError: true}, nil
	}
	content, err := readSkillFile(filepath.Join(skill.Directory, resourcePath), skill.Directory)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: content}, nil
}

func readSkillFile(path string, baseDirectory string) (string, error) {
	base, err := filepath.EvalSymlinks(baseDirectory)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("skill resource escapes its directory")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("skill resource is not a regular file")
	}
	if info.Size() > maxSkillResourceBytes {
		return "", fmt.Errorf("skill resource exceeds %d bytes", maxSkillResourceBytes)
	}
	content, err := os.ReadFile(resolved)
	return string(content), err
}
