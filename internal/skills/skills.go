// Package skills discovers and loads reusable agent skill instructions.
package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const maxSkillMetadataBytes = 1_000_000
const maxAlwaysApplyBytes = 256 * 1024

var validSkillName = regexp.MustCompile(`^[\pL\pN][\pL\pN._-]{0,127}$`)

// Skill describes one discovered SKILL.md file.
type Skill struct {
	Name        string
	Description string
	Path        string
	Directory   string
	Hidden      bool
	AlwaysApply bool
	Globs       []string
	Source      string
}

// Catalog is the effective skill set after project overrides are applied.
type Catalog struct {
	Skills   []Skill
	Warnings []string
}

// Names returns discovered skill names in deterministic order. The returned
// slice is independent of the catalog and can be used by interactive command
// completion without exposing mutable catalog storage.
func (c Catalog) Names() []string {
	names := make([]string, 0, len(c.Skills))
	for _, skill := range c.Skills {
		names = append(names, skill.Name)
	}
	sort.Strings(names)
	return names
}

// Get looks up a skill by its exact name.
func (c Catalog) Get(name string) (Skill, bool) {
	name = strings.TrimSpace(name)
	for _, skill := range c.Skills {
		if skill.Name == name {
			return skill, true
		}
	}
	return Skill{}, false
}

// Load reads a skill's main SKILL.md and removes YAML frontmatter. This is the
// same content shape used by the interactive /skill:<name> command; the model
// facing Skill tool intentionally returns the original file including
// frontmatter so it can inspect metadata when needed.
func (c Catalog) Load(name string) (Skill, string, error) {
	skill, ok := c.Get(name)
	if !ok {
		return Skill{}, "", fmt.Errorf("unknown skill: %s", name)
	}
	content, err := readSkillFile(skill.Path, skill.Directory)
	if err != nil {
		return Skill{}, "", err
	}
	return skill, stripFrontmatter(content), nil
}

// Discover scans standard global and project skill directories.
func Discover(projectRoot string, homeDir string) (Catalog, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return Catalog{}, fmt.Errorf("resolve skill project root: %w", err)
	}
	roots := make([]string, 0)
	seenRoots := make(map[string]bool)
	addRoot := func(value string) {
		if value == "" {
			return
		}
		value = filepath.Clean(value)
		if !seenRoots[value] {
			seenRoots[value] = true
			roots = append(roots, value)
		}
	}
	if homeDir != "" {
		// Compatibility providers are ordered from low to high precedence.
		for _, directory := range []string{".claude", ".codex", ".gemini", ".cursor", ".windsurf", ".opencode", ".banka", ".agent", ".agents", filepath.Join(".pi", "agent"), ".pi", ".omp", filepath.Join(".omp", "agent")} {
			addRoot(filepath.Join(homeDir, filepath.FromSlash(directory), "skills"))
		}
	}
	for _, directory := range []string{".github", ".claude", ".codex", ".gemini", ".cursor", ".windsurf", ".opencode", ".banka", ".agent", ".agents", ".pi", filepath.Join(".pi", "agent"), ".omp", filepath.Join(".omp", "agent")} {
		addRoot(filepath.Join(root, filepath.FromSlash(directory), "skills"))
	}

	byName := make(map[string]Skill)
	realPaths := make(map[string]string)
	var warnings []string
	for _, skillRoot := range roots {
		if _, err := os.Stat(skillRoot); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Catalog{}, err
		}
		rootReal, err := filepath.EvalSymlinks(skillRoot)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("skip inaccessible skill root %s: %v", skillRoot, err))
			continue
		}
		walkErr := filepath.WalkDir(skillRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Name() != "SKILL.md" {
				return nil
			}
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				warnings = append(warnings, fmt.Sprintf("skip inaccessible skill %s: %v", path, resolveErr))
				return nil
			}
			relative, relativeErr := filepath.Rel(rootReal, resolved)
			if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
				warnings = append(warnings, fmt.Sprintf("skip skill %s: resolves outside skill root", path))
				return nil
			}
			if previous, exists := realPaths[resolved]; exists {
				warnings = append(warnings, fmt.Sprintf("skill %s duplicates %s through a symlink; using higher-precedence source", path, previous))
			}
			skill, err := readMetadata(path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("skip invalid skill %s: %v", path, err))
				return nil
			}
			skill.Source = skillRoot
			if previous, exists := byName[skill.Name]; exists && previous.Path != skill.Path {
				warnings = append(warnings, fmt.Sprintf("skill %q from %s overrides %s", skill.Name, skill.Path, previous.Path))
			}
			byName[skill.Name] = skill
			realPaths[resolved] = path
			return nil
		})
		if walkErr != nil {
			return Catalog{}, walkErr
		}
	}

	catalog := Catalog{Skills: make([]Skill, 0, len(byName)), Warnings: warnings}
	for _, skill := range byName {
		catalog.Skills = append(catalog.Skills, skill)
	}
	sort.Slice(catalog.Skills, func(i, j int) bool { return catalog.Skills[i].Name < catalog.Skills[j].Name })
	sort.Strings(catalog.Warnings)
	return catalog, nil
}

// Render formats the skill catalog without eagerly loading skill bodies.
func (c Catalog) Render() string {
	visible := make([]Skill, 0, len(c.Skills))
	for _, skill := range c.Skills {
		if !skill.Hidden {
			visible = append(visible, skill)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	var result strings.Builder
	result.WriteString("Available skills. When a user names one or the task clearly matches its description, call the Skill tool with its name before acting. Read the returned SKILL.md completely and follow it.\n")
	for _, skill := range visible {
		result.WriteString("- ")
		result.WriteString(skill.Name)
		result.WriteString(": ")
		result.WriteString(skill.Description)
		if skill.AlwaysApply {
			result.WriteString(" (always apply)")
		}
		result.WriteByte('\n')
	}
	return result.String()
}

// AlwaysApplyContent loads the bounded bodies of skills marked alwaysApply.
// These skills are trusted local instructions and are injected into the system
// prompt before the lazy catalog. Hidden/disable-model-invocation skills are
// deliberately excluded, and an individual unreadable skill does not prevent
// the agent from starting with the remaining catalog.
func (c Catalog) AlwaysApplyContent() string {
	var result strings.Builder
	used := 0
	for _, skill := range c.Skills {
		if !skill.AlwaysApply || skill.Hidden {
			continue
		}
		content, err := readSkillFile(skill.Path, skill.Directory)
		if err != nil {
			continue
		}
		content = strings.TrimSpace(stripFrontmatter(content))
		if content == "" {
			continue
		}
		section := fmt.Sprintf("[Skill: %s]\n%s\n\n", skill.Name, content)
		if used+len(section) > maxAlwaysApplyBytes {
			remaining := maxAlwaysApplyBytes - used
			if remaining > 0 {
				section = section[:remaining]
				for len(section) > 0 && !utf8.ValidString(section) {
					section = section[:len(section)-1]
				}
				result.WriteString(section)
			}
			break
		}
		result.WriteString(section)
		used += len(section)
	}
	return strings.TrimSpace(result.String())
}

func readMetadata(path string) (Skill, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Skill{}, err
	}
	if !info.Mode().IsRegular() {
		return Skill{}, errors.New("SKILL.md is not a regular file")
	}
	if info.Size() > maxSkillMetadataBytes {
		return Skill{}, fmt.Errorf("SKILL.md exceeds %d bytes", maxSkillMetadataBytes)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	name := filepath.Base(filepath.Dir(path))
	description := "Reusable workflow instructions."
	hidden := false
	alwaysApply := false
	var globs []string
	text := strings.TrimPrefix(string(content), "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) == "---" {
		closing := -1
		for index := 1; index < len(lines); index++ {
			if marker := strings.TrimSpace(strings.TrimSuffix(lines[index], "\r")); marker == "---" || marker == "..." {
				closing = index
				break
			}
		}
		if closing < 0 {
			return Skill{}, errors.New("frontmatter is missing closing ---")
		}
		var metadata map[string]any
		frontmatter := strings.Join(lines[1:closing], "\n")
		if strings.TrimSpace(frontmatter) != "" {
			if err := yaml.Unmarshal([]byte(frontmatter), &metadata); err != nil {
				return Skill{}, fmt.Errorf("parse frontmatter: %w", err)
			}
		}
		if value, ok := metadata["name"].(string); ok && strings.TrimSpace(value) != "" {
			name = value
		}
		if value, ok := metadata["description"].(string); ok && strings.TrimSpace(value) != "" {
			description = value
		}
		hidden = metadataBool(metadata, "hide") || metadataBool(metadata, "disable-model-invocation") || metadataBool(metadata, "disableModelInvocation") || metadataBool(metadata, "disable_model_invocation")
		alwaysApply = metadataBool(metadata, "alwaysApply") || metadataBool(metadata, "always-apply") || metadataBool(metadata, "always_apply")
		globs = metadataStrings(metadata["globs"])
		if len(globs) == 0 {
			globs = metadataStrings(metadata["applyTo"])
		}
	}
	name = strings.TrimSpace(name)
	if !validSkillName.MatchString(name) {
		return Skill{}, fmt.Errorf("invalid skill name %q", name)
	}
	description = strings.Join(strings.Fields(description), " ")
	descriptionRunes := []rune(description)
	if len(descriptionRunes) > 500 {
		description = string(descriptionRunes[:497]) + "..."
	}
	return Skill{Name: name, Description: description, Path: path, Directory: filepath.Dir(path), Hidden: hidden, AlwaysApply: alwaysApply, Globs: globs}, nil
}

func stripFrontmatter(content string) string {
	text := strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != "---" {
		return content
	}
	for index := 1; index < len(lines); index++ {
		marker := strings.TrimSpace(strings.TrimSuffix(lines[index], "\r"))
		if marker == "---" || marker == "..." {
			return strings.Join(lines[index+1:], "\n")
		}
	}
	return content
}

func metadataBool(values map[string]any, key string) bool {
	value, exists := values[key]
	if !exists {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func metadataStrings(value any) []string {
	appendValue := func(result *[]string, text string) {
		for _, item := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '\n' }) {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			duplicate := false
			for _, existing := range *result {
				if existing == item {
					duplicate = true
					break
				}
			}
			if !duplicate {
				*result = append(*result, item)
			}
		}
	}
	result := make([]string, 0)
	switch typed := value.(type) {
	case string:
		appendValue(&result, typed)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				appendValue(&result, text)
			}
		}
	case []string:
		for _, text := range typed {
			appendValue(&result, text)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
