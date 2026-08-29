package lspclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const maxLinterOutputBytes = 4 * 1024 * 1024

// LinterDiagnostics runs a configured CLI-backed linter for one workspace
// file. It is intentionally separate from Client because tools such as
// SwiftLint and Biome expose a CLI reporter rather than a JSON-RPC LSP
// endpoint.
func (m *Manager) LinterDiagnostics(ctx context.Context, name string, server ServerConfig, path string, content string) ([]diagnostic, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	kind := strings.ToLower(strings.TrimSpace(server.Linter))
	if kind == "" {
		return nil, fmt.Errorf("LSP server %q is not a CLI linter", name)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := m.resolveLinterCommand(server)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	path, err = safeWorkspacePath(m.root, path)
	if err != nil {
		return nil, err
	}
	cwd, err := resolveServerCwd(m.root, server.Cwd)
	if err != nil {
		return nil, err
	}
	args := linterDiagnosticArgs(kind, server.Args, path)
	stdout, stderr, exitCode, runErr := runLinter(ctx, resolved, args, cwd, server.Env, content, false)
	if runErr != nil {
		return nil, fmt.Errorf("run %s: %w", kind, runErr)
	}
	// Linters conventionally return a non-zero status when violations exist.
	// Treat non-zero + valid JSON as a successful diagnostic run, but surface a
	// non-zero status with no parseable output as an actual tool failure.
	var diagnostics []diagnostic
	switch kind {
	case "swiftlint":
		diagnostics, err = parseSwiftLintDiagnostics(stdout, path, content, cwd, m.root)
	case "biome":
		diagnostics, err = parseBiomeDiagnostics(stdout, path, content, cwd, m.root)
	default:
		return nil, fmt.Errorf("unsupported CLI linter %q", kind)
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s output: %w", kind, err)
	}
	if exitCode != 0 && len(diagnostics) == 0 && strings.TrimSpace(stdout) == "" {
		if strings.TrimSpace(stderr) == "" {
			return nil, fmt.Errorf("%s exited with status %d", kind, exitCode)
		}
		return nil, fmt.Errorf("%s exited with status %d: %s", kind, exitCode, truncateLinterText(stderr, 1000))
	}
	return diagnostics, nil
}

// LinterFormat returns formatted content for a CLI formatter without mutating
// the target file. The caller decides whether to apply the returned text after
// obtaining the normal LSP write approval.
func (m *Manager) LinterFormat(ctx context.Context, name string, server ServerConfig, path string, content string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !strings.EqualFold(strings.TrimSpace(server.Linter), "biome") {
		return "", fmt.Errorf("%s does not provide CLI formatting", name)
	}
	path, err := safeWorkspacePath(m.root, path)
	if err != nil {
		return "", err
	}
	resolved, err := m.resolveLinterCommand(server)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	cwd, err := resolveServerCwd(m.root, server.Cwd)
	if err != nil {
		return "", err
	}
	args := []string{"format", "--stdin-file-path", path}
	stdout, stderr, exitCode, runErr := runLinter(ctx, resolved, args, cwd, server.Env, content, true)
	if runErr != nil {
		return "", fmt.Errorf("run biome format: %w", runErr)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("biome format exited with status %d: %s", exitCode, truncateLinterText(stderr, 1000))
	}
	if !utf8.ValidString(stdout) {
		return "", errors.New("biome formatter returned invalid UTF-8")
	}
	return stdout, nil
}

func (m *Manager) resolveLinterCommand(server ServerConfig) (string, error) {
	command := strings.TrimSpace(server.ResolvedCommand)
	if command != "" {
		return command, nil
	}
	command = strings.TrimSpace(server.Command)
	if command == "" {
		return "", errors.New("linter command is empty")
	}
	resolved, err := resolveCommand(m.root, command)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func linterDiagnosticArgs(kind string, configured []string, file string) []string {
	args := append([]string(nil), configured...)
	if kind == "biome" {
		// The built-in configuration historically used `lsp-proxy`; that is a
		// JSON-RPC bridge, not a stable linter reporter. Replace it with Biome's
		// JSON CLI mode while preserving user-supplied lint flags.
		if len(args) == 0 || containsArg(args, "lsp-proxy") {
			args = []string{"lint", "--reporter=json"}
		} else if !containsArg(args, "lint") {
			args = append([]string{"lint"}, args...)
		}
		if !containsReporterJSON(args) {
			args = append(args, "--reporter=json")
		}
	} else if kind == "swiftlint" {
		if len(args) == 0 {
			args = []string{"lint", "--quiet", "--reporter", "json"}
		}
		if !containsArg(args, "lint") {
			args = append([]string{"lint"}, args...)
		}
		if !containsReporterJSON(args) {
			args = append(args, "--reporter", "json")
		}
	}
	return appendFileArgument(args, file)
}

func appendFileArgument(args []string, file string) []string {
	for index, argument := range args {
		for _, placeholder := range []string{"${file}", "${FILE}", "$FILE", "{file}", "{{file}}"} {
			if strings.Contains(argument, placeholder) {
				args[index] = strings.ReplaceAll(argument, placeholder, file)
				return args
			}
		}
	}
	return append(args, file)
}

func containsArg(args []string, value string) bool {
	for _, argument := range args {
		if strings.EqualFold(strings.TrimSpace(argument), value) {
			return true
		}
	}
	return false
}

func containsReporterJSON(args []string) bool {
	for index, argument := range args {
		lower := strings.ToLower(strings.TrimSpace(argument))
		if lower == "--reporter=json" || (lower == "--reporter" && index+1 < len(args) && strings.EqualFold(strings.TrimSpace(args[index+1]), "json")) {
			return true
		}
	}
	return false
}

func runLinter(ctx context.Context, command string, args []string, cwd string, overrides map[string]string, stdin string, useStdin bool) (stdout string, stderr string, exitCode int, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	process := exec.CommandContext(ctx, command, args...)
	process.Dir = cwd
	process.Env = mergedEnvironment(overrides)
	if useStdin {
		process.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	var errOut bytes.Buffer
	process.Stdout = &limitedBuffer{buffer: &out, remaining: maxLinterOutputBytes}
	process.Stderr = &limitedBuffer{buffer: &errOut, remaining: maxLinterOutputBytes}
	runErr := process.Run()
	stdout = out.String()
	stderr = errOut.String()
	if runErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdout, stderr, -1, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			return stdout, stderr, exit.ExitCode(), nil
		}
		return stdout, stderr, -1, runErr
	}
	return stdout, stderr, 0, nil
}

type limitedBuffer struct {
	buffer    *bytes.Buffer
	remaining int
}

func (w *limitedBuffer) Write(value []byte) (int, error) {
	if w.remaining > 0 {
		toWrite := value
		if len(toWrite) > w.remaining {
			toWrite = toWrite[:w.remaining]
		}
		_, _ = w.buffer.Write(toWrite)
		w.remaining -= len(toWrite)
	}
	return len(value), nil
}

func parseSwiftLintDiagnostics(output string, target string, content string, base string, root string) ([]diagnostic, error) {
	if strings.TrimSpace(output) == "" {
		return nil, nil
	}
	var violations []struct {
		Character int    `json:"character"`
		File      string `json:"file"`
		Line      int    `json:"line"`
		Reason    string `json:"reason"`
		RuleID    string `json:"rule_id"`
		Severity  string `json:"severity"`
	}
	if err := json.Unmarshal([]byte(output), &violations); err != nil {
		return nil, err
	}
	result := make([]diagnostic, 0, len(violations))
	for _, violation := range violations {
		if !reportedFileMatches(root, target, violation.File, filepath.Dir(target)) {
			continue
		}
		line := maxInt(0, violation.Line-1)
		column := linterUTF16Column(content, line, maxInt(0, violation.Character-1))
		severity := 2
		if strings.EqualFold(violation.Severity, "error") {
			severity = 1
		}
		result = append(result, diagnostic{Range: lspRange{Start: position{Line: line, Character: column}, End: position{Line: line, Character: column}}, Severity: severity, Code: violation.RuleID, Source: "swiftlint", Message: violation.Reason})
	}
	return result, nil
}

func parseBiomeDiagnostics(output string, target string, content string, cwd string, root string) ([]diagnostic, error) {
	if strings.TrimSpace(output) == "" {
		return nil, nil
	}
	var payload struct {
		Diagnostics []struct {
			Category    string          `json:"category"`
			Severity    string          `json:"severity"`
			Message     json.RawMessage `json:"message"`
			Description json.RawMessage `json:"description"`
			Location    *struct {
				Path  string `json:"path"`
				Start *struct {
					Line   int `json:"line"`
					Column int `json:"column"`
				} `json:"start"`
				End *struct {
					Line   int `json:"line"`
					Column int `json:"column"`
				} `json:"end"`
			} `json:"location"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return nil, err
	}
	result := make([]diagnostic, 0, len(payload.Diagnostics))
	for _, item := range payload.Diagnostics {
		if item.Location == nil {
			continue
		}
		if !reportedFileMatches(root, target, item.Location.Path, cwd) {
			continue
		}
		startLine, startColumn := 1, 1
		if item.Location.Start != nil {
			startLine, startColumn = item.Location.Start.Line, item.Location.Start.Column
		}
		endLine, endColumn := startLine, startColumn
		if item.Location.End != nil {
			endLine, endColumn = item.Location.End.Line, item.Location.End.Column
		}
		line := maxInt(0, startLine-1)
		endLineZero := maxInt(0, endLine-1)
		startChar := linterUTF16Column(content, line, maxInt(0, startColumn-1))
		endChar := linterUTF16Column(content, endLineZero, maxInt(0, endColumn-1))
		severity := 2
		switch strings.ToLower(strings.TrimSpace(item.Severity)) {
		case "error":
			severity = 1
		case "info":
			severity = 3
		case "hint":
			severity = 4
		}
		message := biomeMessageText(item.Message)
		if message == "" {
			message = biomeMessageText(item.Description)
		}
		result = append(result, diagnostic{Range: lspRange{Start: position{Line: line, Character: startChar}, End: position{Line: endLineZero, Character: endChar}}, Severity: severity, Code: item.Category, Source: "biome", Message: message})
	}
	return result, nil
}

func biomeMessageText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		// The containing Biome document has already been decoded, so this branch
		// is defensive. Preserve an unfamiliar value instead of rejecting every
		// diagnostic in the reporter output.
		return string(raw)
	}
	return biomeMessageValue(value)
}

func biomeMessageValue(value any) string {
	switch current := value.(type) {
	case string:
		return strings.TrimSpace(current)
	case []any:
		parts := make([]string, 0, len(current))
		for _, item := range current {
			if text := biomeMessageValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		for _, key := range []string{"content", "text", "value", "message", "description"} {
			if nested, exists := current[key]; exists {
				if text := biomeMessageValue(nested); text != "" {
					return text
				}
			}
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

func reportedFileMatches(root string, target string, reported string, base string) bool {
	reported = strings.TrimSpace(reported)
	if reported == "" {
		return true
	}
	reportedPath := reported
	var err error
	if strings.HasPrefix(strings.ToLower(reportedPath), "file:") {
		reportedPath, err = uriToPath(reportedPath)
		if err != nil {
			return false
		}
	} else if !filepath.IsAbs(reportedPath) {
		reportedPath = filepath.Join(base, reportedPath)
	}
	reportedPath, err = safeWorkspacePath(root, reportedPath)
	if err != nil {
		return false
	}
	targetPath, err := safeWorkspacePath(root, target)
	return err == nil && filepath.Clean(reportedPath) == filepath.Clean(targetPath)
}

func linterUTF16Column(content string, line int, column int) int {
	if line < 0 {
		line = 0
	}
	if column < 0 {
		column = 0
	}
	lines := strings.Split(content, "\n")
	if line >= len(lines) {
		return 0
	}
	value := lines[line]
	runes := []rune(value)
	if column > len(runes) {
		column = len(runes)
	}
	return len(utf16.Encode(runes[:column]))
}

func truncateLinterText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return strings.TrimSpace(value)
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value) + "…"
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
