// Package instructions loads hierarchical repository guidance for the agent.
package instructions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxInstructionBytes = 1_000_000

// Document is one loaded instruction file.
type Document struct {
	Path    string
	Content string
}

// Set contains instructions in increasing precedence order.
type Set struct {
	ProjectRoot string
	Documents   []Document
}

// Load reads global and directory-scoped AGENTS instructions.
func Load(workspaceRoot string, homeDir string) (Set, error) {
	workspaceRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return Set{}, err
	}
	projectRoot := findProjectRoot(workspaceRoot)
	var paths []string
	if homeDir != "" {
		paths = append(paths, filepath.Join(homeDir, ".agents", "AGENTS.md"))
	}
	for _, directory := range directoriesBetween(projectRoot, workspaceRoot) {
		override := filepath.Join(directory, "AGENTS.override.md")
		if fileExists(override) {
			paths = append(paths, override)
			continue
		}
		paths = append(paths, filepath.Join(directory, "AGENTS.md"))
	}

	set := Set{ProjectRoot: projectRoot}
	seen := make(map[string]bool)
	for _, path := range paths {
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		content, err := readOptionalFile(path, maxInstructionBytes)
		if err != nil {
			return Set{}, fmt.Errorf("load instructions %s: %w", path, err)
		}
		if strings.TrimSpace(content) != "" {
			set.Documents = append(set.Documents, Document{Path: path, Content: content})
		}
	}
	return set, nil
}

// Render formats loaded documents for inclusion in the system prompt.
func (s Set) Render() string {
	if len(s.Documents) == 0 {
		return ""
	}
	var result strings.Builder
	result.WriteString("The following AGENTS instructions apply in order. Later files take precedence when instructions conflict.\n")
	for _, document := range s.Documents {
		result.WriteString("\n<agents path=\"")
		result.WriteString(document.Path)
		result.WriteString("\">\n")
		result.WriteString(strings.TrimSpace(document.Content))
		result.WriteString("\n</agents>\n")
	}
	return result.String()
}

func findProjectRoot(workspaceRoot string) string {
	current := workspaceRoot
	for {
		if fileExists(filepath.Join(current, ".git")) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return workspaceRoot
		}
		current = parent
	}
}

func directoriesBetween(root string, leaf string) []string {
	var reversed []string
	current := leaf
	for {
		reversed = append(reversed, current)
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return []string{leaf}
		}
		current = parent
	}
	result := make([]string, len(reversed))
	for index := range reversed {
		result[len(reversed)-1-index] = reversed[index]
	}
	return result
}

func readOptionalFile(path string, limit int64) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", nil
	}
	if info.Size() > limit {
		return "", fmt.Errorf("file exceeds %d bytes", limit)
	}
	content, err := os.ReadFile(path)
	return string(content), err
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
