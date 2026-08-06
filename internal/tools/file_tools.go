package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxReadFileBytes = 1_000_000
const defaultReadLines = 2_000
const maxReadLines = 5_000

type readTool struct{}
type writeTool struct{}
type editTool struct{}

// NewReadTool creates the Read tool.
func NewReadTool() Definition { return readTool{} }

// NewWriteTool creates the Write tool.
func NewWriteTool() Definition { return writeTool{} }

// NewEditTool creates the Edit tool.
func NewEditTool() Definition { return editTool{} }

func (readTool) Name() string        { return "Read" }
func (readTool) Description() string { return "Read a text file from the current workspace." }
func (readTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"path": stringSchema("Path to the file relative to the workspace root."),
		"offset": JSONSchema{
			"type": "integer", "description": "One-based line number to start at. Defaults to 1.", "minimum": 1,
		},
		"limit": JSONSchema{
			"type": "integer", "description": "Maximum lines to return. Defaults to 2000.", "minimum": 1, "maximum": maxReadLines,
		},
	}, "path")
}
func (readTool) Execute(_ context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	pathValue, err := requireString(arguments, "path")
	if err != nil {
		return Result{}, fmt.Errorf("Read tool requires a non-empty 'path' string.")
	}

	absolutePath, err := ResolveSafePath(toolContext.WorkspaceRoot, pathValue)
	if err != nil {
		return Result{}, err
	}

	info, err := os.Stat(absolutePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("File does not exist: %s", pathValue)
		}
		return Result{}, err
	}
	if info.Size() > maxReadFileBytes {
		return Result{}, fmt.Errorf("File is too large to read safely: %s (%d bytes).", pathValue, info.Size())
	}

	content, err := os.ReadFile(absolutePath)
	if err != nil {
		return Result{}, err
	}
	offset, err := optionalInteger(arguments, "offset", 1, 1, int(^uint(0)>>1))
	if err != nil {
		return Result{}, fmt.Errorf("Read tool requires 'offset' to be a positive integer.")
	}
	limit, err := optionalInteger(arguments, "limit", defaultReadLines, 1, maxReadLines)
	if err != nil {
		return Result{}, fmt.Errorf("Read tool requires 'limit' to be an integer between 1 and %d.", maxReadLines)
	}
	lines := strings.SplitAfter(string(content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return Result{Content: ""}, nil
	}
	if offset > len(lines) {
		return Result{}, fmt.Errorf("Read offset %d exceeds file length of %d lines.", offset, len(lines))
	}
	end := min(len(lines), offset-1+limit)
	result := strings.Join(lines[offset-1:end], "")
	if end < len(lines) {
		result += fmt.Sprintf("\n[truncated: showing lines %d-%d of %d]", offset, end, len(lines))
	}
	return Result{Content: result}, nil
}

func (writeTool) Name() string { return "Write" }
func (writeTool) Description() string {
	return "Create or overwrite a text file in the current workspace."
}
func (writeTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"path":    stringSchema("Path to the file relative to the workspace root."),
		"content": stringSchema("Full text content to write."),
	}, "path", "content")
}
func (writeTool) Execute(_ context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	pathValue, err := requireString(arguments, "path")
	if err != nil {
		return Result{}, fmt.Errorf("Write tool requires a non-empty 'path' string.")
	}
	content, ok := arguments["content"].(string)
	if !ok {
		return Result{}, fmt.Errorf("Write tool requires a string 'content' field.")
	}

	absolutePath, err := ResolveSafePath(toolContext.WorkspaceRoot, pathValue)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(absolutePath, []byte(content), 0o644); err != nil {
		return Result{}, err
	}
	return Result{Content: "Wrote file: " + pathValue}, nil
}

func (editTool) Name() string        { return "Edit" }
func (editTool) Description() string { return "Replace a unique text fragment inside a file." }
func (editTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"path":    stringSchema("Path to the file relative to the workspace root."),
		"oldText": stringSchema("Existing text that must appear exactly once."),
		"newText": stringSchema("Replacement text."),
	}, "path", "oldText", "newText")
}
func (editTool) Execute(_ context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	pathValue, err := requireString(arguments, "path")
	if err != nil {
		return Result{}, fmt.Errorf("Edit tool requires a non-empty 'path' string.")
	}
	oldText, ok := arguments["oldText"].(string)
	if !ok || oldText == "" {
		return Result{}, fmt.Errorf("Edit tool requires a non-empty 'oldText' string.")
	}
	newText, ok := arguments["newText"].(string)
	if !ok {
		return Result{}, fmt.Errorf("Edit tool requires a string 'newText' field.")
	}

	absolutePath, err := ResolveSafePath(toolContext.WorkspaceRoot, pathValue)
	if err != nil {
		return Result{}, err
	}
	contentBytes, err := os.ReadFile(absolutePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("File does not exist: %s", pathValue)
		}
		return Result{}, err
	}
	content := string(contentBytes)
	count := strings.Count(content, oldText)
	if count == 0 {
		return Result{}, fmt.Errorf("Target text was not found in: %s", pathValue)
	}
	if count > 1 {
		return Result{}, fmt.Errorf("Target text must appear exactly once in: %s", pathValue)
	}

	next := strings.Replace(content, oldText, newText, 1)
	if err := os.WriteFile(absolutePath, []byte(next), 0o644); err != nil {
		return Result{}, err
	}
	return Result{Content: "Edited file: " + pathValue}, nil
}

func requireString(arguments map[string]any, key string) (string, error) {
	value, ok := arguments[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("missing string %s", key)
	}
	return value, nil
}

func optionalInteger(arguments map[string]any, key string, fallback int, minimum int, maximum int) (int, error) {
	value, exists := arguments[key]
	if !exists {
		return fallback, nil
	}
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) || int(number) < minimum || int(number) > maximum {
		return 0, fmt.Errorf("invalid integer %s", key)
	}
	return int(number), nil
}
