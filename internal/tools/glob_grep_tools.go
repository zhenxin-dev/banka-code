package tools

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxGlobResults = 100
const maxGrepResults = 200
const maxGrepFileBytes = 1_000_000

type globTool struct{}
type grepTool struct{}

// NewGlobTool creates the Glob tool.
func NewGlobTool() Definition { return globTool{} }

// NewGrepTool creates the Grep tool.
func NewGrepTool() Definition { return grepTool{} }

func (globTool) Name() string { return "Glob" }
func (globTool) Description() string {
	return "Find files by glob pattern within paths permitted by the current access mode."
}
func (globTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"pattern": stringSchema("Glob pattern to match files, such as '**/*.ts'."),
		"path":    stringSchema("Directory to search. Outside-workspace paths require full-access or YOLO mode."),
	}, "pattern")
}
func (globTool) Execute(_ context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	pattern, err := requireString(arguments, "pattern")
	if err != nil {
		return Result{}, fmt.Errorf("Glob tool requires a non-empty 'pattern' string.")
	}
	searchRoot := toolContext.WorkspaceRoot
	pathValue, hasPath, err := optionalNonEmptyString(arguments, "path")
	if err != nil {
		return Result{}, fmt.Errorf("Glob tool requires 'path' to be a non-empty string when provided.")
	}
	if hasPath {
		searchRoot, err = toolContext.ResolvePath(pathValue)
		if err != nil {
			return Result{}, err
		}
	}
	info, err := os.Stat(searchRoot)
	if err != nil {
		return Result{}, fmt.Errorf("Path does not exist: %s", displayPath(arguments))
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("Glob tool requires a directory path: %s", displayPath(arguments))
	}

	matches, truncated, err := collectGlobMatches(toolContext.WorkspaceRoot, searchRoot, pattern, maxGlobResults)
	if err != nil {
		return Result{}, err
	}
	if len(matches) == 0 {
		return Result{Content: "No files matched the pattern."}, nil
	}
	content := strings.Join(matches, "\n")
	if truncated {
		content += "\n\n[truncated]"
	}
	return Result{Content: content}, nil
}

func (grepTool) Name() string { return "Grep" }
func (grepTool) Description() string {
	return "Search file contents within paths permitted by the current access mode."
}
func (grepTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"pattern": stringSchema("Regular expression pattern to search for."),
		"path":    stringSchema("Directory or file to search. Outside-workspace paths require full-access or YOLO mode."),
		"include": stringSchema("Glob pattern used to filter searched files, such as '*.ts'. Defaults to '**/*'."),
		"outputMode": JSONSchema{
			"type": "string", "description": "Output mode. Defaults to 'content'.",
			"enum": []string{"content", "files_with_matches"},
		},
	}, "pattern")
}
func (grepTool) Execute(_ context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	pattern, err := requireString(arguments, "pattern")
	if err != nil {
		return Result{}, fmt.Errorf("Grep tool requires a non-empty 'pattern' string.")
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return Result{}, fmt.Errorf("Invalid grep pattern: %s", pattern)
	}
	outputMode := "content"
	if value, ok, argumentErr := optionalNonEmptyString(arguments, "outputMode"); argumentErr != nil {
		return Result{}, fmt.Errorf("Grep tool requires 'outputMode' to be 'content' or 'files_with_matches'.")
	} else if ok {
		if value != "content" && value != "files_with_matches" {
			return Result{}, fmt.Errorf("Grep tool requires 'outputMode' to be 'content' or 'files_with_matches'.")
		}
		outputMode = value
	}
	include := "**/*"
	if value, ok, argumentErr := optionalNonEmptyString(arguments, "include"); argumentErr != nil {
		return Result{}, fmt.Errorf("Grep tool requires 'include' to be a non-empty string when provided.")
	} else if ok {
		include = value
	}

	searchRoot := toolContext.WorkspaceRoot
	pathValue, hasPath, argumentErr := optionalNonEmptyString(arguments, "path")
	if argumentErr != nil {
		return Result{}, fmt.Errorf("Grep tool requires 'path' to be a non-empty string when provided.")
	}
	if hasPath {
		searchRoot, err = toolContext.ResolvePath(pathValue)
		if err != nil {
			return Result{}, err
		}
	}
	info, err := os.Stat(searchRoot)
	if err != nil {
		return Result{}, fmt.Errorf("Path does not exist: %s", displayPath(arguments))
	}

	var results []string
	seen := map[string]bool{}
	if info.IsDir() {
		files, _, err := collectGlobMatches(toolContext.WorkspaceRoot, searchRoot, include, 0)
		if err != nil {
			return Result{}, err
		}
		for _, relativeFile := range files {
			absoluteFile := filepath.Join(toolContext.WorkspaceRoot, relativeFile)
			appendGrepMatches(&results, seen, absoluteFile, toolContext.WorkspaceRoot, matcher, outputMode)
			if len(results) >= maxGrepResults {
				return Result{Content: strings.Join(results[:maxGrepResults], "\n") + "\n\n[truncated]"}, nil
			}
		}
	} else {
		appendGrepMatches(&results, seen, searchRoot, toolContext.WorkspaceRoot, matcher, outputMode)
	}

	if len(results) == 0 {
		return Result{Content: "No matches found."}, nil
	}
	return Result{Content: strings.Join(results, "\n")}, nil
}

func collectGlobMatches(workspaceRoot string, searchRoot string, pattern string, limit int) ([]string, bool, error) {
	var matches []string
	truncated := false
	err := filepath.WalkDir(searchRoot, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relativeToSearch, err := filepath.Rel(searchRoot, filePath)
		if err != nil {
			return err
		}
		if !matchGlob(pattern, filepath.ToSlash(relativeToSearch)) {
			return nil
		}
		relativeToWorkspace, err := filepath.Rel(workspaceRoot, filePath)
		if err != nil {
			return err
		}
		matches = append(matches, filepath.ToSlash(relativeToWorkspace))
		if limit > 0 && len(matches) >= limit {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(matches)
	return matches, truncated, err
}

func matchGlob(pattern string, name string) bool {
	pattern = filepath.ToSlash(pattern)
	if ok, _ := path.Match(pattern, name); ok {
		return true
	}
	regex := globToRegexp(pattern)
	matched, _ := regexp.MatchString(regex, name)
	return matched
}

func globToRegexp(pattern string) string {
	var result strings.Builder
	result.WriteString("^")
	for index := 0; index < len(pattern); {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				index += 2
				if index < len(pattern) && pattern[index] == '/' {
					result.WriteString("(?:.*/)?")
					index++
				} else {
					result.WriteString(".*")
				}
			} else {
				result.WriteString("[^/]*")
				index++
			}
		case '?':
			result.WriteString("[^/]")
			index++
		default:
			result.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
			index++
		}
	}
	result.WriteString("$")
	return result.String()
}

func appendGrepMatches(results *[]string, seen map[string]bool, absolutePath string, workspaceRoot string, matcher *regexp.Regexp, outputMode string) {
	info, err := os.Stat(absolutePath)
	if err != nil || info.Size() > maxGrepFileBytes {
		return
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		return
	}
	defer file.Close()

	relativePath, err := filepath.Rel(workspaceRoot, absolutePath)
	if err != nil {
		return
	}
	relativePath = filepath.ToSlash(relativePath)

	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.ContainsRune(line, '\x00') {
			return
		}
		lineNumber++
		if !matcher.MatchString(line) {
			continue
		}
		if outputMode == "files_with_matches" {
			if !seen[relativePath] {
				*results = append(*results, relativePath)
				seen[relativePath] = true
			}
			return
		}
		*results = append(*results, fmt.Sprintf("%s:%d: %s", relativePath, lineNumber, line))
	}
}

func optionalString(arguments map[string]any, key string) (string, bool) {
	value, exists := arguments[key]
	if !exists {
		return "", false
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

func optionalNonEmptyString(arguments map[string]any, key string) (string, bool, error) {
	value, exists := arguments[key]
	if !exists {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", false, fmt.Errorf("%s must be a non-empty string", key)
	}
	return text, true, nil
}

func displayPath(arguments map[string]any) string {
	if value, ok := optionalString(arguments, "path"); ok {
		return value
	}
	return "."
}
