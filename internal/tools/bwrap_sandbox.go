package tools

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

var (
	bwrapCheckOnce sync.Once
	bwrapAvailable bool
)

var minimalEnvironmentKeys = []string{"HOME", "PATH", "TMPDIR", "LANG", "TERM"}
var readonlyBindDirectories = []string{"/usr", "/bin", "/lib", "/lib64", "/etc"}

func isBubblewrapAvailable() bool {
	bwrapCheckOnce.Do(func() {
		path, err := exec.LookPath("bwrap")
		if err != nil {
			return
		}
		bwrapAvailable = exec.Command(path, "--version").Run() == nil
	})
	return bwrapAvailable
}

func newShellCommand(ctx context.Context, command string, workspaceRoot string) *exec.Cmd {
	if isBubblewrapAvailable() {
		return exec.CommandContext(ctx, "bwrap", buildBwrapArgs(command, workspaceRoot)...)
	}

	return newDirectShellCommand(ctx, command)
}

func newDirectShellCommand(ctx context.Context, command string) *exec.Cmd {
	shell := "zsh"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "bash"
	}
	cmd := exec.CommandContext(ctx, shell, "-lc", command)
	cmd.Env = sanitizedChildEnvironment(nil)
	return cmd
}

func sanitizedChildEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && !strings.HasPrefix(key, "BANKA_") {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func buildBwrapArgs(command string, workspaceRoot string) []string {
	args := []string{
		"--unshare-all",
		"--new-session",
		"--die-with-parent",
		"--clearenv",
		"--proc", "/proc",
		"--dev", "/dev",
	}

	for _, directory := range readonlyBindDirectories {
		if _, err := os.Stat(directory); err == nil {
			args = append(args, "--ro-bind", directory, directory)
		}
	}
	args = append(args, "--tmpfs", "/tmp")
	args = append(args, "--bind", workspaceRoot, workspaceRoot)
	for _, pair := range collectMinimalEnvironment() {
		args = append(args, "--setenv", pair[0], pair[1])
	}
	args = append(args, "zsh", "-lc", command)
	return args
}

func collectMinimalEnvironment() [][2]string {
	var result [][2]string
	seen := make(map[string]bool)
	for _, key := range minimalEnvironmentKeys {
		if value := os.Getenv(key); value != "" {
			result = append(result, [2]string{key, value})
			seen[key] = true
		}
	}
	return result
}
