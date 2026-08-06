package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveSafePath resolves targetPath and rejects paths outside workspaceRoot.
func ResolveSafePath(workspaceRoot string, targetPath string) (string, error) {
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}

	candidate := targetPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	if !isPathWithin(root, candidate) {
		return "", fmt.Errorf("Path escapes workspace root: %s", targetPath)
	}

	existing := candidate
	for {
		_, statErr := os.Lstat(existing)
		if statErr == nil {
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", statErr
		}
		existing = parent
	}
	resolvedExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if !isPathWithin(resolvedRoot, resolvedExisting) {
		return "", fmt.Errorf("Path resolves outside workspace root: %s", targetPath)
	}
	return candidate, nil
}

func isPathWithin(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)))
}
