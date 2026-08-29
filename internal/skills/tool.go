package skills

import (
	"context"
	"errors"
	"fmt"
	"net/url"
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

// Tool is the model-facing skill loader. MarkLoaded lets an interactive
// /skill invocation share its lifecycle with subsequent skill:// resource
// reads, because the main SKILL.md was already injected into that turn.
type Tool interface {
	tools.Definition
	MarkLoaded(name string) error
}

// ReadURI resolves a mounted skill URI for generic host tools such as Read.
// It deliberately reuses the Skill tool's loaded-skill gate, so exposing the
// URI through another tool cannot bypass the SKILL.md-first workflow.
func (t *skillTool) ReadURI(ctx context.Context, uri string) (string, error) {
	result, err := t.Execute(ctx, map[string]any{"uri": uri}, tools.Context{})
	if err != nil {
		return "", err
	}
	if result.IsError {
		return "", errors.New(result.Content)
	}
	return result.Content, nil
}

// NewTool creates the on-demand Skill loader.
func NewTool(catalog Catalog) Tool {
	byName := make(map[string]Skill, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		byName[skill.Name] = skill
	}
	return &skillTool{byName: byName, loaded: make(map[string]bool)}
}

// MarkLoaded records that the main SKILL.md was loaded through another
// trusted entry point, such as the interactive /skill:<name> command.
func (t *skillTool) MarkLoaded(name string) error {
	name = strings.TrimSpace(name)
	if _, ok := t.byName[name]; !ok {
		return fmt.Errorf("unknown skill: %s", name)
	}
	t.mu.Lock()
	t.loaded[name] = true
	t.mu.Unlock()
	return nil
}

func (*skillTool) Name() string { return "Skill" }
func (*skillTool) Description() string {
	return "Load a discovered SKILL.md completely, or load one of its referenced resources after the main skill has been loaded. Supports skill://<name>/<path> URIs."
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
			"uri": map[string]any{
				"type": "string", "description": "Optional skill://<name>/<relative-path> URI (an alternative to name/resource).",
			},
			"resource": map[string]any{
				"type": "string", "description": "Optional path relative to the skill directory. Load SKILL.md first.",
			},
		},
		// Keep the schema permissive enough for both the historical
		// {name,resource} form and the URI form used by OMP-compatible clients.
		// Execute performs the precise one-of validation and path checks.
		"anyOf":                []any{map[string]any{"required": []string{"name"}}, map[string]any{"required": []string{"uri"}}},
		"additionalProperties": false,
	}
}
func (t *skillTool) Execute(ctx context.Context, arguments map[string]any, _ tools.Context) (tools.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return tools.Result{}, err
	}
	nameValue, nameProvided := arguments["name"]
	name, nameIsString := nameValue.(string)
	name = strings.TrimSpace(name)
	if nameProvided && !nameIsString {
		return tools.Result{}, errors.New("Skill tool 'name' must be a string")
	}
	resourceRaw, resourceProvided := arguments["resource"]
	resourceValue, resourceIsString := resourceRaw.(string)
	resourceValue = strings.TrimSpace(resourceValue)
	if resourceProvided && !resourceIsString {
		return tools.Result{}, errors.New("Skill tool 'resource' must be a string when provided")
	}
	uriRaw, uriProvided := arguments["uri"]
	uriValue, uriIsString := uriRaw.(string)
	if uriProvided && !uriIsString {
		return tools.Result{}, errors.New("Skill tool 'uri' must be a string when provided")
	}
	resourceAlreadyDecoded := false
	if strings.TrimSpace(uriValue) != "" {
		uriName, uriResource, uriErr := parseSkillURI(uriValue)
		if uriErr != nil {
			return tools.Result{}, uriErr
		}
		if name != "" && name != uriName {
			return tools.Result{}, errors.New("Skill 'name' does not match the skill:// URI")
		}
		name = uriName
		if resourceValue != "" {
			return tools.Result{}, errors.New("Skill URI cannot be combined with a separate 'resource'")
		}
		resourceValue = uriResource
		resourceAlreadyDecoded = true
	}
	if strings.HasPrefix(strings.ToLower(resourceValue), "skill://") {
		uriName, uriResource, uriErr := parseSkillURI(resourceValue)
		if uriErr != nil {
			return tools.Result{}, uriErr
		}
		if name != "" && name != uriName {
			return tools.Result{}, errors.New("Skill 'name' does not match the skill:// resource URI")
		}
		name = uriName
		resourceValue = uriResource
		resourceAlreadyDecoded = true
	}
	if name == "" {
		return tools.Result{}, errors.New("Skill tool requires a non-empty 'name' or 'uri'")
	}
	skill, ok := t.byName[name]
	if !ok {
		return tools.Result{}, fmt.Errorf("unknown skill: %s", name)
	}
	if resourceValue == "" {
		content, err := readSkillFile(skill.Path, skill.Directory)
		if err != nil {
			return tools.Result{}, err
		}
		t.mu.Lock()
		t.loaded[name] = true
		t.mu.Unlock()
		return tools.Result{Content: fmt.Sprintf("Skill directory: %s\n\n%s", skill.Directory, content)}, nil
	}
	t.mu.Lock()
	loaded := t.loaded[name]
	t.mu.Unlock()
	if !loaded {
		return tools.Result{Content: "Load the skill's SKILL.md before requesting its resources.", IsError: true}, nil
	}
	decodedPath := resourceValue
	var err error
	if !resourceAlreadyDecoded {
		decodedPath, err = url.PathUnescape(resourceValue)
		if err != nil {
			return tools.Result{}, fmt.Errorf("invalid skill resource path: %w", err)
		}
	}
	content, err := readSkillFile(filepath.Join(skill.Directory, decodedPath), skill.Directory)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: content}, nil
}

// parseSkillURI parses the small skill:// URL space.  The host is the exact
// discovered skill name and the path is relative to that skill directory.
// Query strings, fragments, user-info, and ports are rejected so a URI cannot
// smuggle an unrelated network resource into the file-backed loader.
func parseSkillURI(value string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", "", fmt.Errorf("invalid skill URI: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "skill") || parsed.Host == "" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("skill URI must use skill://<name>/<relative-path>")
	}
	name, err := url.PathUnescape(parsed.Host)
	if err != nil || strings.TrimSpace(name) == "" {
		return "", "", errors.New("skill URI contains an invalid skill name")
	}
	resource := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if resource == "" || resource == "SKILL.md" {
		return name, "", nil
	}
	decoded, err := url.PathUnescape(resource)
	if err != nil {
		return "", "", fmt.Errorf("invalid skill URI path: %w", err)
	}
	decodedPath := filepath.Clean(filepath.FromSlash(decoded))
	if strings.ContainsRune(decoded, '\x00') || filepath.IsAbs(decodedPath) || decodedPath == ".." || strings.HasPrefix(decodedPath, ".."+string(filepath.Separator)) {
		return "", "", errors.New("skill URI path escapes its directory")
	}
	return name, decodedPath, nil
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
