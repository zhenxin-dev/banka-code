package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const maxPatchBytes = 1_000_000

type applyPatchTool struct{}

// NewApplyPatchTool creates an atomic unified-diff patch tool.
func NewApplyPatchTool() Definition { return applyPatchTool{} }

func (applyPatchTool) Name() string { return "ApplyPatch" }
func (applyPatchTool) Description() string {
	return "Apply a unified diff inside the workspace. The complete patch is validated before any file is changed."
}
func (applyPatchTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"patch": stringSchema("Unified diff using workspace-relative a/ and b/ paths."),
	}, "patch")
}
func (applyPatchTool) Execute(ctx context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	patch, err := requireString(arguments, "patch")
	if err != nil {
		return Result{}, errors.New("ApplyPatch tool requires a non-empty 'patch' string")
	}
	if len(patch) > maxPatchBytes {
		return Result{}, fmt.Errorf("patch exceeds %d bytes", maxPatchBytes)
	}
	if err := validatePatchPaths(patch, toolContext.WorkspaceRoot); err != nil {
		return Result{}, err
	}
	if strings.Contains(patch, "new file mode 120000") || strings.Contains(patch, "old mode 120000") {
		return Result{}, errors.New("symbolic-link patches are not allowed")
	}
	check := exec.CommandContext(ctx, "git", "apply", "--check", "--whitespace=nowarn", "-")
	check.Dir = toolContext.WorkspaceRoot
	check.Stdin = strings.NewReader(patch)
	var checkOutput bytes.Buffer
	check.Stdout = &checkOutput
	check.Stderr = &checkOutput
	if err := check.Run(); err != nil {
		return Result{Content: "Patch validation failed:\n" + checkOutput.String(), IsError: true}, nil
	}
	apply := exec.CommandContext(ctx, "git", "apply", "--whitespace=nowarn", "-")
	apply.Dir = toolContext.WorkspaceRoot
	apply.Stdin = strings.NewReader(patch)
	var applyOutput bytes.Buffer
	apply.Stdout = &applyOutput
	apply.Stderr = &applyOutput
	if err := apply.Run(); err != nil {
		return Result{Content: "Patch application failed after validation:\n" + applyOutput.String(), IsError: true}, nil
	}
	return Result{Content: "Applied patch successfully."}, nil
}

func validatePatchPaths(patch string, workspaceRoot string) error {
	for _, line := range strings.Split(patch, "\n") {
		var value string
		switch {
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			parsed, err := parsePatchPath(line[4:])
			if err != nil {
				return err
			}
			value = parsed
		default:
			continue
		}
		if value == "/dev/null" {
			continue
		}
		value = strings.TrimPrefix(strings.TrimPrefix(value, "a/"), "b/")
		cleaned := filepath.Clean(value)
		if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("patch path escapes workspace: %s", value)
		}
		if _, err := ResolveSafePath(workspaceRoot, cleaned); err != nil {
			return err
		}
	}
	return nil
}

func parsePatchPath(value string) (string, error) {
	if index := strings.IndexByte(value, '\t'); index >= 0 {
		value = value[:index]
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("patch contains an empty file path")
	}
	if strings.HasPrefix(value, "\"") {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid quoted patch path: %w", err)
		}
		return unquoted, nil
	}
	return value, nil
}
