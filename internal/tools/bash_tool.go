package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const maxOutputLength = 12_000
const defaultBashTimeout = 30 * time.Second
const maxBashTimeout = 10 * time.Minute

const (
	sandboxPermissionDefault   = "use_default"
	sandboxPermissionEscalated = "require_escalated"
)

type bashTool struct{}

// NewBashTool creates the Bash tool.
func NewBashTool() Definition { return bashTool{} }

func (bashTool) Name() string { return "Bash" }
func (bashTool) Description() string {
	return "Execute a shell command. Commands run in an offline workspace sandbox by default. Use require_escalated with a concise justification when network or access outside the workspace is necessary."
}
func (bashTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"command": stringSchema("Shell command to execute."),
		"timeout_ms": JSONSchema{
			"type": "integer", "description": "Timeout in milliseconds. Defaults to 30000.",
			"minimum": 1000, "maximum": maxBashTimeout.Milliseconds(),
		},
		"sandbox_permissions": JSONSchema{
			"type": "string", "description": "Use require_escalated only when the default offline workspace sandbox is insufficient.",
			"enum": []string{sandboxPermissionDefault, sandboxPermissionEscalated},
		},
		"justification": stringSchema("Why elevated execution is necessary. Required with require_escalated."),
	}, "command")
}
func (bashTool) Execute(parent context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	command, err := requireString(arguments, "command")
	if err != nil {
		return Result{}, fmt.Errorf("Bash tool requires a non-empty 'command' string.")
	}
	timeout, err := bashTimeout(arguments)
	if err != nil {
		return Result{}, err
	}
	permissions, err := bashSandboxPermissions(arguments)
	if err != nil {
		return Result{}, err
	}

	fullAccess := toolContext.PermissionMode().HasFullAccess()
	if permissions == sandboxPermissionEscalated && !fullAccess {
		justification, justificationErr := requireString(arguments, "justification")
		if justificationErr != nil {
			return Result{}, fmt.Errorf("Bash tool requires a non-empty 'justification' with require_escalated.")
		}
		allowed, approvalErr := toolContext.RequestPermission(parent, ApprovalRequest{
			ToolName: "Bash", Kind: ApprovalHost, Scope: "bash:host", Command: command, Justification: justification,
		})
		if approvalErr != nil {
			return Result{}, approvalErr
		}
		if !allowed {
			return Result{Content: "User denied elevated execution.", IsError: true}, nil
		}
	} else if !fullAccess {
		if validationError := validateCommand(command, toolContext.WorkspaceRoot); validationError != "" {
			return Result{Content: validationError, IsError: true}, nil
		}
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if permissions == sandboxPermissionEscalated || fullAccess {
		cmd = newDirectShellCommand(ctx, command)
	} else {
		cmd = newShellCommand(ctx, command, toolContext.WorkspaceRoot)
	}
	cmd.Dir = toolContext.WorkspaceRoot

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	timedOut := ctx.Err() == context.DeadlineExceeded
	content := truncateOutput(formatProcessResult(stdout.String(), stderr.String(), exitCode, timedOut, timeout))
	return Result{Content: content, IsError: err != nil || timedOut}, nil
}

func bashTimeout(arguments map[string]any) (time.Duration, error) {
	value, exists := arguments["timeout_ms"]
	if !exists {
		return defaultBashTimeout, nil
	}
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) {
		return 0, fmt.Errorf("Bash tool requires 'timeout_ms' to be an integer.")
	}
	timeout := time.Duration(number) * time.Millisecond
	if timeout < time.Second || timeout > maxBashTimeout {
		return 0, fmt.Errorf("Bash tool requires 'timeout_ms' between 1000 and %d.", maxBashTimeout.Milliseconds())
	}
	return timeout, nil
}

func bashSandboxPermissions(arguments map[string]any) (string, error) {
	value, exists := arguments["sandbox_permissions"]
	if !exists {
		return sandboxPermissionDefault, nil
	}
	permissions, ok := value.(string)
	if !ok || (permissions != sandboxPermissionDefault && permissions != sandboxPermissionEscalated) {
		return "", fmt.Errorf("Bash tool requires 'sandbox_permissions' to be 'use_default' or 'require_escalated'.")
	}
	return permissions, nil
}

func formatProcessResult(stdout string, stderr string, exitCode int, timedOut bool, timeout time.Duration) string {
	parts := []string{}
	if timedOut {
		parts = append(parts, fmt.Sprintf("timeout_ms: %d", timeout.Milliseconds()))
		parts = append(parts, "exit_code: terminated")
	} else {
		parts = append(parts, fmt.Sprintf("exit_code: %d", exitCode))
	}
	if strings.TrimSpace(stdout) != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	return strings.Join(parts, "\n\n")
}

func truncateOutput(value string) string {
	if len(value) <= maxOutputLength {
		return value
	}
	return value[:maxOutputLength] + "\n\n[truncated]"
}
