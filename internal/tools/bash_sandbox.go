package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	windowsAbsolutePathPattern = regexp.MustCompile(`[A-Za-z]:[\\/]`)
	privilegeCommandPattern    = regexp.MustCompile(`\b(sudo|su)\b`)
	dangerousEnvPattern        = regexp.MustCompile(`\b(?:export\s+)?(PATH|HOME|LD_PRELOAD|LD_LIBRARY_PATH|DYLD_INSERT_LIBRARIES|SHELL|USER|LOGNAME)=`)
	redirectPattern            = regexp.MustCompile(`>>?\s*([^\s;|&>]+)`)
	commandTokenPattern        = regexp.MustCompile("[\\s;|&<>()$`'\"\\\\]+")
	parentPathPattern          = regexp.MustCompile(`(?:^|/)\.\.(?:/|$)`)
)

func validateCommand(command string, workspaceRoot string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ""
	}

	if match := windowsAbsolutePathPattern.FindString(trimmed); match != "" {
		return fmt.Sprintf("Windows absolute paths are not allowed: %s...", match)
	}
	if match := privilegeCommandPattern.FindString(trimmed); match != "" {
		return "Privilege escalation commands (sudo, su) are not allowed."
	}
	if match := dangerousEnvPattern.FindStringSubmatch(trimmed); len(match) > 1 {
		return fmt.Sprintf("Manipulating environment variable '%s' is not allowed.", match[1])
	}
	for _, match := range redirectPattern.FindAllStringSubmatch(trimmed, -1) {
		if len(match) > 1 && isPathEscape(match[1], workspaceRoot) {
			return fmt.Sprintf("Redirect target escapes workspace: %s", match[1])
		}
	}
	for _, token := range commandTokenPattern.Split(trimmed, -1) {
		if token != "" && isPathEscape(token, workspaceRoot) {
			return fmt.Sprintf("Path argument escapes workspace: %s", token)
		}
	}
	return ""
}

func isPathEscape(pathValue string, workspaceRoot string) bool {
	if filepath.IsAbs(pathValue) {
		return !isWithinWorkspace(pathValue, workspaceRoot)
	}
	if parentPathPattern.MatchString(filepath.ToSlash(pathValue)) {
		return !isWithinWorkspace(filepath.Join(workspaceRoot, pathValue), workspaceRoot)
	}
	return false
}

func isWithinWorkspace(pathValue string, workspaceRoot string) bool {
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return false
	}
	resolved, err := filepath.Abs(pathValue)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
