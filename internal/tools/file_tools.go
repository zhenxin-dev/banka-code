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

func (readTool) Name() string { return "Read" }
func (readTool) Description() string {
	return "Read a text file or a mounted internal URI (for example skill://name/path) permitted by the current access mode."
}
func (readTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"path": stringSchema("File path or mounted internal URI such as skill://name/path. Outside-workspace paths require full-access or YOLO mode."),
		"offset": JSONSchema{
			"type": "integer", "description": "One-based line number to start at. Defaults to 1.", "minimum": 1,
		},
		"limit": JSONSchema{
			"type": "integer", "description": "Maximum lines to return. Defaults to 2000.", "minimum": 1, "maximum": maxReadLines,
		},
	}, "path")
}
func (readTool) Execute(ctx context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	pathValue, err := requireString(arguments, "path")
	if err != nil {
		return Result{}, fmt.Errorf("Read tool requires a non-empty 'path' string.")
	}
	offset, err := optionalInteger(arguments, "offset", 1, 1, int(^uint(0)>>1))
	if err != nil {
		return Result{}, fmt.Errorf("Read tool requires 'offset' to be a positive integer.")
	}
	limit, err := optionalInteger(arguments, "limit", defaultReadLines, 1, maxReadLines)
	if err != nil {
		return Result{}, fmt.Errorf("Read tool requires 'limit' to be an integer between 1 and %d.", maxReadLines)
	}

	// Internal URIs are not filesystem paths. Resolve them through the mounted
	// host reader before calling ResolvePath so a scheme cannot accidentally be
	// interpreted as a relative filename.
	if schemeEnd := strings.Index(pathValue, "://"); schemeEnd >= 0 {
		if !strings.EqualFold(pathValue[:schemeEnd], "skill") {
			return Result{}, fmt.Errorf("Read does not support URI scheme in %q", pathValue)
		}
		if toolContext.URIReader == nil {
			return Result{}, errors.New("Read cannot resolve skill:// without a mounted skill reader")
		}
		content, readErr := toolContext.URIReader.ReadURI(ctx, pathValue)
		if readErr != nil {
			return Result{}, readErr
		}
		formatted, formatErr := formatReadLines(content, offset, limit)
		if formatErr != nil {
			return Result{}, formatErr
		}
		return Result{Content: formatted}, nil
	}

	absolutePath, err := toolContext.ResolvePath(pathValue)
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
	formatted, err := formatReadLines(string(content), offset, limit)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: formatted}, nil
}

func formatReadLines(content string, offset int, limit int) (string, error) {
	lines := strings.SplitAfter(string(content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return "", nil
	}
	if offset > len(lines) {
		return "", fmt.Errorf("Read offset %d exceeds file length of %d lines.", offset, len(lines))
	}
	end := min(len(lines), offset-1+limit)
	result := strings.Join(lines[offset-1:end], "")
	if end < len(lines) {
		result += fmt.Sprintf("\n[truncated: showing lines %d-%d of %d]", offset, end, len(lines))
	}
	return result, nil
}

func (writeTool) Name() string { return "Write" }
func (writeTool) Description() string {
	return "Create or overwrite a text file permitted by the current access mode."
}
func (writeTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"path":    stringSchema("File path. Outside-workspace paths require full-access or YOLO mode."),
		"content": stringSchema("Full text content to write."),
	}, "path", "content")
}
func (writeTool) Execute(ctx context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	pathValue, err := requireString(arguments, "path")
	if err != nil {
		return Result{}, fmt.Errorf("Write tool requires a non-empty 'path' string.")
	}
	content, ok := arguments["content"].(string)
	if !ok {
		return Result{}, fmt.Errorf("Write tool requires a string 'content' field.")
	}

	absolutePath, err := toolContext.ResolvePath(pathValue)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		return Result{}, err
	}
	operation := "write"
	if _, err := os.Stat(absolutePath); errors.Is(err, os.ErrNotExist) {
		operation = "create"
	}
	if err := os.WriteFile(absolutePath, []byte(content), 0o644); err != nil {
		return Result{}, err
	}
	result := Result{Content: "Wrote file: " + pathValue}
	appendFileObserverFeedback(ctx, toolContext, []FileChange{{Path: absolutePath, Operation: operation}}, &result)
	return result, nil
}

func (editTool) Name() string        { return "Edit" }
func (editTool) Description() string { return "Replace a unique text fragment inside a file." }
func (editTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"path":    stringSchema("File path. Outside-workspace paths require full-access or YOLO mode."),
		"oldText": stringSchema("Existing text that must appear exactly once."),
		"newText": stringSchema("Replacement text."),
	}, "path", "oldText", "newText")
}
func (editTool) Execute(ctx context.Context, arguments map[string]any, toolContext Context) (Result, error) {
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

	absolutePath, err := toolContext.ResolvePath(pathValue)
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
	result := Result{Content: "Edited file: " + pathValue}
	appendFileObserverFeedback(ctx, toolContext, []FileChange{{Path: absolutePath, Operation: "edit"}}, &result)
	return result, nil
}

func appendFileObserverFeedback(ctx context.Context, toolContext Context, changes []FileChange, result *Result) {
	if toolContext.FileObserver == nil || len(changes) == 0 {
		return
	}
	feedback, err := toolContext.FileObserver.AfterFileChanges(ctx, changes)
	if err != nil {
		result.Content += "\n\n[LSP warning: " + err.Error() + "]"
		return
	}
	if strings.TrimSpace(feedback) != "" {
		result.Content += "\n\n" + feedback
	}
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
