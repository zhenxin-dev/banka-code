// Package skills discovers and loads reusable agent skill instructions.
package skills

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill describes one discovered SKILL.md file.
type Skill struct {
	Name        string
	Description string
	Path        string
	Directory   string
}

// Catalog is the effective skill set after project overrides are applied.
type Catalog struct {
	Skills []Skill
}

// Discover scans standard global and project skill directories.
func Discover(projectRoot string, homeDir string) (Catalog, error) {
	roots := []string{}
	if homeDir != "" {
		roots = append(roots,
			filepath.Join(homeDir, ".banka", "skills"),
			filepath.Join(homeDir, ".agents", "skills"),
		)
	}
	roots = append(roots,
		filepath.Join(projectRoot, ".banka", "skills"),
		filepath.Join(projectRoot, ".agents", "skills"),
	)

	byName := make(map[string]Skill)
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Catalog{}, err
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Name() != "SKILL.md" {
				return nil
			}
			skill, err := readMetadata(path)
			if err != nil {
				return err
			}
			byName[skill.Name] = skill
			return nil
		})
		if err != nil {
			return Catalog{}, err
		}
	}

	catalog := Catalog{Skills: make([]Skill, 0, len(byName))}
	for _, skill := range byName {
		catalog.Skills = append(catalog.Skills, skill)
	}
	sort.Slice(catalog.Skills, func(i, j int) bool { return catalog.Skills[i].Name < catalog.Skills[j].Name })
	return catalog, nil
}

// Render formats the skill catalog without eagerly loading skill bodies.
func (c Catalog) Render() string {
	if len(c.Skills) == 0 {
		return ""
	}
	var result strings.Builder
	result.WriteString("Available skills. When a user names one or the task clearly matches its description, call the Skill tool with its name before acting. Read the returned SKILL.md completely and follow it.\n")
	for _, skill := range c.Skills {
		result.WriteString("- ")
		result.WriteString(skill.Name)
		result.WriteString(": ")
		result.WriteString(skill.Description)
		result.WriteByte('\n')
	}
	return result.String()
}

func readMetadata(path string) (Skill, error) {
	file, err := os.Open(path)
	if err != nil {
		return Skill{}, err
	}
	defer file.Close()

	name := filepath.Base(filepath.Dir(path))
	description := "Reusable workflow instructions."
	scanner := bufio.NewScanner(file)
	if scanner.Scan() && strings.TrimSpace(scanner.Text()) == "---" {
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "---" {
				break
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), "\"'")
			switch strings.TrimSpace(key) {
			case "name":
				if value != "" {
					name = value
				}
			case "description":
				if value != "" {
					description = value
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Skill{}, err
	}
	return Skill{Name: name, Description: description, Path: path, Directory: filepath.Dir(path)}, nil
}
