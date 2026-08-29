package lspclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhenxin-dev/banka-code/internal/tools"
)

const (
	maxLSPResultBytes                  = 48_000
	maxDiagnosticItems                 = 200
	singleDiagnosticsWaitTimeout       = 3 * time.Second
	batchDiagnosticsWaitTimeout        = 400 * time.Millisecond
	writeThroughDiagnosticsWaitTimeout = 300 * time.Millisecond
)

type tool struct {
	manager *Manager
}

// NewTool creates an LSP tool from a manager.
func NewTool(manager *Manager) tools.Definition {
	if manager == nil {
		return nil
	}
	return &tool{manager: manager}
}

func (*tool) Name() string { return "LSP" }

func (*tool) Description() string {
	return "Use configured Language Servers for diagnostics, navigation, hover, symbols, rename, code actions, formatting, capabilities, and raw requests. Servers start lazily."
}

func (*tool) InputSchema() tools.JSONSchema {
	return tools.JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "description": "LSP operation to perform.", "enum": []string{
				"status", "diagnostics", "definition", "type_definition", "implementation", "references", "hover", "symbols", "rename", "rename_file", "code_actions", "formatting", "format", "capabilities", "reload", "request",
			}},
			"file":       map[string]any{"type": "string", "description": "Workspace-relative file path. Use '*' for workspace operations."},
			"line":       map[string]any{"type": "integer", "minimum": 1, "description": "One-based source line."},
			"symbol":     map[string]any{"type": "string", "description": "Symbol text on the selected line; supports name#N."},
			"new_name":   map[string]any{"type": "string", "description": "New identifier or destination path for rename operations."},
			"query":      map[string]any{"type": "string", "description": "Workspace symbol/code-action query or raw method name."},
			"apply":      map[string]any{"type": "boolean", "description": "Apply edits. Rename/formatting default to true; code actions default to false."},
			"timeout_ms": map[string]any{"type": "integer", "minimum": 1000, "maximum": 300000, "description": "Request timeout in milliseconds."},
			"timeout":    map[string]any{"type": "number", "minimum": 1, "maximum": 300, "description": "Request timeout in seconds (alias for timeout_ms)."},
			"payload":    map[string]any{"type": "string", "description": "JSON parameters for action=request."},
		},
		"required": []string{"action"}, "additionalProperties": false,
	}
}

func (t *tool) Execute(parent context.Context, arguments map[string]any, toolContext tools.Context) (tools.Result, error) {
	if parent == nil {
		parent = context.Background()
	}
	toolContext = t.withWorkspaceContext(toolContext)
	action, ok := arguments["action"].(string)
	if !ok || strings.TrimSpace(action) == "" {
		return tools.Result{}, errors.New("LSP requires a non-empty 'action' string")
	}
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "typedefinition", "type-definition":
		action = "type_definition"
	case "renamesymbol", "rename-symbol":
		action = "rename"
	case "renamefile", "rename-file":
		action = "rename_file"
	case "codeactions", "code-actions":
		action = "code_actions"
	}
	switch action {
	case "status":
		return t.statusResult(), nil
	case "capabilities":
		return t.capabilities(parent, arguments, toolContext)
	case "reload":
		return t.reload(parent, arguments, toolContext)
	case "request":
		return t.rawRequest(parent, arguments, toolContext)
	}
	fileArgument := strings.TrimSpace(stringValue(arguments, "file"))
	if action == "diagnostics" && (fileArgument == "*" || hasGlobPattern(fileArgument)) {
		return t.workspaceDiagnostics(parent, arguments, toolContext)
	}
	if action == "symbols" && (fileArgument == "*" || (fileArgument == "" && strings.TrimSpace(stringValue(arguments, "query")) != "")) {
		return t.workspaceSymbols(parent, arguments, toolContext)
	}
	fileValue, ok := arguments["file"].(string)
	if !ok || strings.TrimSpace(fileValue) == "" {
		return tools.Result{}, fmt.Errorf("LSP action %q requires a non-empty 'file'", action)
	}
	if fileValue == "*" {
		return tools.Result{}, fmt.Errorf("LSP action %q does not support workspace file '*'", action)
	}
	pathValue, err := toolContext.ResolvePath(fileValue)
	if err != nil {
		return tools.Result{}, err
	}
	// LSP sessions are workspace-scoped even when the surrounding Banka
	// session has full host access. Do not send outside-workspace contents to a
	// language server configured for this project.
	if pathValue, err = safeWorkspacePath(t.manager.root, pathValue); err != nil {
		return tools.Result{}, err
	}
	info, statErr := os.Stat(pathValue)
	if action == "rename_file" {
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return tools.Result{}, fmt.Errorf("LSP file does not exist: %s", fileValue)
			}
			return tools.Result{}, fmt.Errorf("stat LSP rename source %s: %w", fileValue, statErr)
		}
		return t.renameFile(parent, pathValue, arguments, toolContext, fileValue)
	}
	if statErr != nil || info.IsDir() {
		if statErr != nil {
			return tools.Result{}, fmt.Errorf("LSP file does not exist: %s", fileValue)
		}
		return tools.Result{}, fmt.Errorf("LSP file must be a regular file: %s", fileValue)
	}
	if action == "diagnostics" {
		return t.fileDiagnostics(parent, pathValue, fileValue, toolContext, arguments)
	}
	serverName, server, err := t.manager.ServerForFile(pathValue)
	if err != nil {
		return tools.Result{}, err
	}
	content, err := os.ReadFile(pathValue)
	if err != nil {
		return tools.Result{}, fmt.Errorf("read LSP file %s: %w", fileValue, err)
	}
	if server.Linter != "" {
		if action == "formatting" || action == "format" {
			return t.linterFormatting(parent, serverName, server, pathValue, content, arguments, toolContext, fileValue)
		}
		return tools.Result{}, fmt.Errorf("LSP server %q is a CLI linter and does not support %s", serverName, action)
	}
	client, _, err := t.manager.Client(parent, serverName, server)
	if err != nil {
		return tools.Result{}, err
	}
	uri, err := client.OpenDocument(parent, pathValue, string(content))
	if err != nil {
		return tools.Result{}, err
	}
	timeout := lspTimeout(arguments)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	line, err := optionalLine(arguments)
	if err != nil {
		return tools.Result{}, err
	}
	position := position{Line: line - 1, Character: 0}
	if line > 0 {
		column, columnErr := resolveSymbolColumn(string(content), line, stringValue(arguments, "symbol"))
		if columnErr != nil {
			return tools.Result{}, columnErr
		}
		position.Character = column
	}
	params := map[string]any{"textDocument": map[string]any{"uri": uri}, "position": position}
	switch action {
	case "diagnostics":
		return t.diagnostics(ctx, client, uri, fileValue, singleDiagnosticsWaitTimeout)
	case "definition", "type_definition", "implementation", "references", "hover":
		return t.navigation(ctx, client, action, params, fileValue)
	case "symbols":
		return t.documentSymbols(ctx, client, uri, fileValue)
	case "rename":
		return t.rename(ctx, client, params, arguments, toolContext, fileValue)
	case "rename_file":
		// Handled before the single-file routing above so directory renames can
		// enumerate all affected files.
		return tools.Result{}, errors.New("internal error: rename_file was not routed")
	case "code_actions":
		return t.codeActions(ctx, client, params, arguments, toolContext, fileValue)
	case "formatting", "format":
		return t.formatting(ctx, client, uri, arguments, toolContext, fileValue)
	default:
		return tools.Result{}, fmt.Errorf("unsupported LSP action: %s (server %s)", action, server.Command)
	}
}

func (t *tool) fileDiagnostics(parent context.Context, pathValue string, displayPath string, toolContext tools.Context, arguments map[string]any) (tools.Result, error) {
	matches := t.manager.ServersForFile(pathValue)
	if len(matches) == 0 {
		return tools.Result{}, fmt.Errorf("no configured language server handles %s", displayPath)
	}
	ctx, cancel := context.WithTimeout(parent, lspTimeout(arguments))
	defer cancel()
	content, err := os.ReadFile(pathValue)
	if err != nil {
		return tools.Result{}, fmt.Errorf("read LSP file %s: %w", displayPath, err)
	}
	var outputs []string
	var failures []string
	hasErrors := false
	for _, match := range matches {
		if match.Config.Linter != "" {
			values, lintErr := t.manager.LinterDiagnostics(ctx, match.Name, match.Config, pathValue, string(content))
			if lintErr != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", match.Name, lintErr))
				continue
			}
			if len(values) == 0 {
				continue
			}
			var lines []string
			for index, item := range values {
				if index >= maxDiagnosticItems {
					break
				}
				lines = append(lines, fmt.Sprintf("%s %s:%d:%d: %s", diagnosticSeverity(item.Severity), displayPath,
					item.Range.Start.Line+1, item.Range.Start.Character+1, item.Message))
			}
			outputs = append(outputs, fmt.Sprintf("[%s]\nFound %d diagnostic(s):\n%s", match.Name, len(values), strings.Join(lines, "\n")))
			hasErrors = hasErrors || hasErrorDiagnostic(values)
			continue
		}
		client, _, clientErr := t.manager.Client(ctx, match.Name, match.Config)
		if clientErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", match.Name, clientErr))
			continue
		}
		uri, openErr := client.OpenDocument(ctx, pathValue, string(content))
		if openErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", match.Name, openErr))
			continue
		}
		_ = client.Notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}, "text": string(content)})
		result, diagnosticsErr := t.diagnostics(ctx, client, uri, displayPath, singleDiagnosticsWaitTimeout)
		if diagnosticsErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", match.Name, diagnosticsErr))
			continue
		}
		if result.Content != "OK" {
			outputs = append(outputs, fmt.Sprintf("[%s]\n%s", match.Name, result.Content))
			hasErrors = hasErrors || result.IsError
		}
	}
	if len(outputs) == 0 && len(failures) == 0 {
		return tools.Result{Content: "OK"}, nil
	}
	if len(outputs) == 0 && len(failures) > 0 {
		return tools.Result{Content: "LSP diagnostics failed: " + strings.Join(failures, "; "), IsError: true}, nil
	}
	if len(failures) > 0 {
		outputs = append(outputs, "[server warnings]\n"+strings.Join(failures, "\n"))
	}
	return tools.Result{Content: strings.Join(outputs, "\n"), IsError: hasErrors}, nil
}

func (t *tool) statusResult() tools.Result {
	statuses := t.manager.Statuses()
	if len(statuses) == 0 {
		return tools.Result{Content: "No language servers configured for this workspace."}
	}
	var lines []string
	for _, status := range statuses {
		state := "configured, not started"
		if !status.Available {
			state = "unavailable"
			if status.UnavailableReason != "" {
				state += ": " + status.UnavailableReason
			}
		} else if status.Running {
			state = fmt.Sprintf("running (%d open, %d diagnostics)", status.OpenDocuments, status.Diagnostics)
		}
		if status.Error != "" {
			state += "; last error: " + status.Error
		}
		lines = append(lines, fmt.Sprintf("%s — %s", status.Name, state))
	}
	return tools.Result{Content: "Language servers:\n" + strings.Join(lines, "\n")}
}

func (t *tool) capabilities(parent context.Context, arguments map[string]any, toolContext tools.Context) (tools.Result, error) {
	servers := t.selectedServerNames(arguments, toolContext)
	if len(servers) == 0 {
		return tools.Result{Content: "No language servers configured for this workspace."}, nil
	}
	timeout := lspTimeout(arguments)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result := make(map[string]any)
	for _, name := range servers {
		server, exists := t.manager.serverConfig(name)
		if !exists {
			result[name] = map[string]any{"error": "language server configuration disappeared during reload"}
			continue
		}
		if server.Disabled || server.ResolvedCommand == "" {
			result[name] = map[string]any{"error": server.UnavailableReason}
			continue
		}
		if server.Linter != "" {
			capabilities := map[string]any{
				"diagnosticProvider": map[string]any{"kind": "full", "interFileDependencies": false},
				"linter":             server.Linter,
			}
			if strings.EqualFold(server.Linter, "biome") {
				capabilities["documentFormattingProvider"] = true
			}
			result[name] = capabilities
			continue
		}
		client, _, err := t.manager.Client(ctx, name, server)
		if err != nil {
			result[name] = map[string]any{"error": err.Error()}
			continue
		}
		result[name] = client.Capabilities()
	}
	return jsonTextResult(result)
}

func (t *tool) reload(parent context.Context, arguments map[string]any, toolContext tools.Context) (tools.Result, error) {
	ctx, cancel := context.WithTimeout(parent, lspTimeout(arguments))
	defer cancel()
	if err := approveLSPMutation(ctx, toolContext, "reload language server", "lsp:reload"); err != nil {
		return tools.Result{}, err
	}
	file := strings.TrimSpace(stringValue(arguments, "file"))
	if file == "" || file == "*" {
		var lines []string
		for _, name := range t.manager.Config().Names() {
			if err := t.manager.Reload(ctx, name); err != nil {
				lines = append(lines, fmt.Sprintf("Failed to reload %s: %v", name, err))
			} else {
				lines = append(lines, "Reloaded "+name)
			}
		}
		if len(lines) == 0 {
			return tools.Result{Content: "No available language servers to reload."}, nil
		}
		return tools.Result{Content: strings.Join(lines, "\n")}, nil
	}
	pathValue, err := toolContext.ResolvePath(file)
	if err != nil {
		return tools.Result{}, err
	}
	if pathValue, err = safeWorkspacePath(t.manager.root, pathValue); err != nil {
		return tools.Result{}, err
	}
	name, _, err := t.manager.ServerForFile(pathValue)
	if err != nil {
		return tools.Result{}, err
	}
	if err := t.manager.Reload(ctx, name); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: "Reloaded " + name}, nil
}

func (t *tool) diagnostics(ctx context.Context, client *Client, uri string, file string, waitTimeout time.Duration) (tools.Result, error) {
	// Servers using pull diagnostics can answer directly; push servers publish
	// asynchronously and are covered by WaitForDiagnostics below.
	var pulled struct {
		Kind        string       `json:"kind"`
		Items       []diagnostic `json:"items"`
		Diagnostics []diagnostic `json:"diagnostics"`
	}
	if capabilityEnabled(client.Capabilities()["diagnosticProvider"]) {
		if err := client.Request(ctx, "textDocument/diagnostic", map[string]any{"textDocument": map[string]any{"uri": uri}}, &pulled); err == nil {
			// A successful pull response is authoritative, including an empty list;
			// retaining an older push result would report stale diagnostics forever.
			values := pulled.Items
			if pulled.Items == nil && pulled.Diagnostics != nil {
				values = pulled.Diagnostics
			}
			client.setDiagnostics(uri, values)
		} else if !isMethodNotFound(err) {
			// A server that advertises pull diagnostics but returns a real protocol
			// error should not be reported as a clean file.  Push-only servers commonly
			// answer -32601, which remains the one intentionally ignored case.
			return tools.Result{}, fmt.Errorf("LSP diagnostics pull failed: %w", err)
		}
	}
	diagnostics := client.WaitForDiagnostics(ctx, uri, waitTimeout)
	if len(diagnostics) == 0 {
		return tools.Result{Content: "OK"}, nil
	}
	if len(diagnostics) > maxDiagnosticItems {
		diagnostics = diagnostics[:maxDiagnosticItems]
	}
	var lines []string
	for _, item := range diagnostics {
		severity := diagnosticSeverity(item.Severity)
		lines = append(lines, fmt.Sprintf("%s %s:%d:%d: %s", severity, file, item.Range.Start.Line+1, item.Range.Start.Character+1, item.Message))
	}
	return tools.Result{Content: fmt.Sprintf("Found %d diagnostic(s):\n%s", len(diagnostics), strings.Join(lines, "\n")), IsError: hasErrorDiagnostic(diagnostics)}, nil
}

func (t *tool) workspaceDiagnostics(parent context.Context, arguments map[string]any, toolContext tools.Context) (tools.Result, error) {
	pattern := strings.TrimSpace(stringValue(arguments, "file"))
	if pattern == "" {
		return tools.Result{}, errors.New("LSP diagnostics requires 'file'; use '*' for workspace diagnostics")
	}
	paths, err := t.diagnosticTargets(pattern, toolContext)
	if err != nil {
		return tools.Result{}, err
	}
	if len(paths) == 0 {
		return tools.Result{Content: "No files matched for diagnostics."}, nil
	}
	ctx, cancel := context.WithTimeout(parent, lspTimeout(arguments))
	defer cancel()
	var lines []string
	errorCount := 0
	attempts := 0
	successes := 0
	var failures []string
	for _, target := range paths {
		if err := ctx.Err(); err != nil {
			return tools.Result{}, err
		}
		content, readErr := os.ReadFile(target)
		if readErr != nil {
			continue
		}
		matches := t.manager.ServersForFile(target)
		for _, match := range matches {
			attempts++
			if match.Config.Linter != "" {
				values, lintErr := t.manager.LinterDiagnostics(ctx, match.Name, match.Config, target, string(content))
				if lintErr != nil {
					failures = append(failures, fmt.Sprintf("%s (%s): %v", match.Name, displayWorkspacePath(t.manager.root, target), lintErr))
					continue
				}
				successes++
				if len(values) == 0 {
					continue
				}
				if hasErrorDiagnostic(values) {
					errorCount++
				}
				var diagnosticLines []string
				for index, item := range values {
					if index >= maxDiagnosticItems {
						break
					}
					diagnosticLines = append(diagnosticLines, fmt.Sprintf("%s %s:%d:%d: %s", diagnosticSeverity(item.Severity), displayWorkspacePath(t.manager.root, target), item.Range.Start.Line+1, item.Range.Start.Character+1, item.Message))
				}
				lines = append(lines, fmt.Sprintf("[%s]\nFound %d diagnostic(s):\n%s", match.Name, len(values), strings.Join(diagnosticLines, "\n")))
				continue
			}
			client, _, clientErr := t.manager.Client(ctx, match.Name, match.Config)
			if clientErr != nil {
				failures = append(failures, fmt.Sprintf("%s (%s): %v", match.Name, displayWorkspacePath(t.manager.root, target), clientErr))
				continue
			}
			uri, openErr := client.OpenDocument(ctx, target, string(content))
			if openErr != nil {
				failures = append(failures, fmt.Sprintf("%s (%s): %v", match.Name, displayWorkspacePath(t.manager.root, target), openErr))
				continue
			}
			_ = client.Notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}, "text": string(content)})
			result, diagnosticsErr := t.diagnostics(ctx, client, uri, displayWorkspacePath(t.manager.root, target), batchDiagnosticsWaitTimeout)
			if diagnosticsErr != nil || result.Content == "OK" {
				if diagnosticsErr != nil {
					failures = append(failures, fmt.Sprintf("%s (%s): %v", match.Name, displayWorkspacePath(t.manager.root, target), diagnosticsErr))
				} else {
					successes++
				}
				continue
			}
			successes++
			if result.IsError {
				errorCount++
			}
			lines = append(lines, fmt.Sprintf("[%s]\n%s", match.Name, result.Content))
		}
	}
	if len(lines) == 0 {
		if successes == 0 && len(failures) > 0 {
			return tools.Result{Content: "Workspace diagnostics failed: " + strings.Join(failures, "; "), IsError: true}, nil
		}
		if len(failures) > 0 {
			return tools.Result{Content: "OK\nServer warnings: " + strings.Join(failures, "; ")}, nil
		}
		return tools.Result{Content: "OK"}, nil
	}
	if len(failures) > 0 {
		lines = append(lines, "Server warnings: "+strings.Join(failures, "; "))
	}
	return tools.Result{Content: strings.Join(lines, "\n"), IsError: errorCount > 0 || (attempts > 0 && successes == 0)}, nil
}

func (t *tool) diagnosticTargets(pattern string, toolContext tools.Context) ([]string, error) {
	root, err := filepath.Abs(t.manager.root)
	if err != nil {
		return nil, err
	}
	if pattern != "*" {
		cleanPattern := filepath.Clean(filepath.FromSlash(pattern))
		if filepath.IsAbs(cleanPattern) || cleanPattern == ".." || strings.HasPrefix(cleanPattern, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("diagnostics pattern escapes workspace: %s", pattern)
		}
		// Validate the glob before walking so a malformed pattern is reported even
		// when the workspace contains no matching files.  matchWorkspaceGlob also
		// implements the recursive `**` semantics users expect from workspace
		// globs (path.Match alone treats `**` as an ordinary `*`).
		if _, matchErr := matchWorkspaceGlob(pattern, ""); matchErr != nil {
			return nil, fmt.Errorf("invalid diagnostics glob %q: %w", pattern, matchErr)
		}
	}
	var result []string
	seen := make(map[string]struct{})
	walkErr := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if current != root && (name == ".git" || name == ".banka" || name == ".agents" || name == ".claude" || name == ".codex" || name == "node_modules" || name == ".venv" || name == "venv") {
				return filepath.SkipDir
			}
			return nil
		}
		if len(result) >= maxDiagnosticItems {
			return filepath.SkipDir
		}
		relative, relErr := filepath.Rel(root, current)
		if relErr != nil {
			return relErr
		}
		if pattern != "*" {
			matched, matchErr := matchWorkspaceGlob(pattern, relative)
			if matchErr != nil {
				return fmt.Errorf("invalid diagnostics glob %q: %w", pattern, matchErr)
			}
			if !matched {
				return nil
			}
		}
		if _, resolveErr := toolContext.ResolvePath(relative); resolveErr != nil {
			return nil
		}
		if _, exists := seen[current]; !exists {
			seen[current] = struct{}{}
			if pattern == "*" {
				if _, _, serverErr := t.manager.ServerForFile(current); serverErr != nil {
					return nil
				}
			}
			result = append(result, current)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(result)
	return result, nil
}

// matchWorkspaceGlob matches slash-separated workspace paths.  Go's
// path.Match intentionally gives `*` no special directory semantics; a small
// segment matcher lets us support the conventional recursive `**` token while
// retaining path.Match's character classes and escaping rules for each
// ordinary segment.
func matchWorkspaceGlob(pattern string, value string) (bool, error) {
	pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
	value = strings.TrimPrefix(filepath.ToSlash(value), "./")
	patternParts := splitGlobPath(pattern)
	valueParts := splitGlobPath(value)
	for _, part := range patternParts {
		if part == "**" {
			continue
		}
		if _, err := path.Match(part, ""); err != nil {
			return false, err
		}
	}
	type key struct{ patternIndex, valueIndex int }
	memo := make(map[key]bool)
	known := make(map[key]bool)
	var match func(int, int) bool
	match = func(patternIndex, valueIndex int) bool {
		state := key{patternIndex, valueIndex}
		if known[state] {
			return memo[state]
		}
		known[state] = true
		if patternIndex == len(patternParts) {
			memo[state] = valueIndex == len(valueParts)
			return memo[state]
		}
		part := patternParts[patternIndex]
		if part == "**" {
			// Prefer zero segments, then consume one segment and stay on `**`.
			memo[state] = match(patternIndex+1, valueIndex) || (valueIndex < len(valueParts) && match(patternIndex, valueIndex+1))
			return memo[state]
		}
		if valueIndex >= len(valueParts) {
			return false
		}
		matched, err := path.Match(part, valueParts[valueIndex])
		memo[state] = err == nil && matched && match(patternIndex+1, valueIndex+1)
		return memo[state]
	}
	return match(0, 0), nil
}

func splitGlobPath(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" {
		return nil
	}
	parts := strings.Split(value, "/")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func hasGlobPattern(value string) bool {
	return strings.ContainsAny(value, "*?[")
}

func (t *tool) navigation(ctx context.Context, client *Client, action string, params map[string]any, file string) (tools.Result, error) {
	method := map[string]string{"definition": "textDocument/definition", "type_definition": "textDocument/typeDefinition", "implementation": "textDocument/implementation", "references": "textDocument/references", "hover": "textDocument/hover"}[action]
	if action == "references" {
		params["context"] = map[string]any{"includeDeclaration": true}
	}
	var raw json.RawMessage
	if err := client.Request(ctx, method, params, &raw); err != nil {
		return tools.Result{}, fmt.Errorf("LSP %s failed: %w", action, err)
	}
	if action == "hover" {
		text := extractHoverText(raw)
		if text == "" || text == "null" {
			return tools.Result{Content: "No hover information."}, nil
		}
		return tools.Result{Content: truncateLSP(text)}, nil
	}
	locations, err := decodeLocations(raw)
	if err != nil {
		return tools.Result{}, err
	}
	if len(locations) == 0 {
		return tools.Result{Content: "No " + navigationLabel(action) + " found."}, nil
	}
	var lines []string
	for _, item := range locations {
		path, pathErr := uriToPath(item.URI)
		if pathErr != nil {
			return tools.Result{}, fmt.Errorf("decode LSP %s location %q: %w", action, item.URI, pathErr)
		}
		if path, pathErr = safeWorkspacePath(t.manager.root, path); pathErr != nil {
			return tools.Result{}, fmt.Errorf("LSP %s location is outside the workspace: %w", action, pathErr)
		}
		path = displayWorkspacePath(t.manager.root, path)
		lines = append(lines, fmt.Sprintf("%s:%d:%d", path, item.Range.Start.Line+1, item.Range.Start.Character+1))
	}
	return tools.Result{Content: fmt.Sprintf("Found %d %s:\n%s", len(locations), navigationLabel(action), strings.Join(lines, "\n"))}, nil
}

func (t *tool) documentSymbols(ctx context.Context, client *Client, uri string, file string) (tools.Result, error) {
	var raw json.RawMessage
	if err := client.Request(ctx, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": uri}}, &raw); err != nil {
		return tools.Result{}, fmt.Errorf("LSP symbols failed: %w", err)
	}
	var hierarchical []documentSymbol
	if len(raw) == 0 || string(raw) == "null" {
		return tools.Result{Content: "No symbols found."}, nil
	}
	if isHierarchicalSymbolResponse(raw) && json.Unmarshal(raw, &hierarchical) == nil {
		var lines []string
		for _, symbol := range hierarchical {
			appendDocumentSymbolLines(&lines, symbol, 0)
		}
		if len(lines) > 0 {
			return tools.Result{Content: "Symbols in " + file + ":\n" + strings.Join(lines, "\n")}, nil
		}
	}
	var flat []symbolInformation
	if err := json.Unmarshal(raw, &flat); err != nil {
		return tools.Result{}, fmt.Errorf("decode document symbols: %w", err)
	}
	var lines []string
	for _, symbol := range flat {
		lines = append(lines, fmt.Sprintf("%s @ %d:%d", symbol.Name, symbol.Location.Range.Start.Line+1, symbol.Location.Range.Start.Character+1))
	}
	if len(lines) == 0 {
		return tools.Result{Content: "No symbols found."}, nil
	}
	return tools.Result{Content: "Symbols in " + file + ":\n" + strings.Join(lines, "\n")}, nil
}

func (t *tool) workspaceSymbols(ctx context.Context, arguments map[string]any, _ tools.Context) (tools.Result, error) {
	requestContext, cancel := context.WithTimeout(ctx, lspTimeout(arguments))
	defer cancel()
	query := stringValue(arguments, "query")
	if strings.TrimSpace(query) == "" {
		return tools.Result{}, errors.New("workspace symbols require a non-empty 'query'")
	}
	var lines []string
	var failures []string
	config := t.manager.Config()
	for _, name := range config.Names() {
		server := config.Servers[name]
		if server.Linter != "" {
			continue
		}
		client, _, err := t.manager.Client(requestContext, name, server)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		var raw json.RawMessage
		if err := client.Request(requestContext, "workspace/symbol", map[string]any{"query": query}, &raw); err != nil {
			if !isMethodNotFound(err) {
				failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			}
			continue
		}
		var symbols []symbolInformation
		if json.Unmarshal(raw, &symbols) != nil {
			continue
		}
		for _, symbol := range symbols {
			if !strings.Contains(strings.ToLower(symbol.Name), strings.ToLower(query)) {
				continue
			}
			path, pathErr := uriToPath(symbol.Location.URI)
			if pathErr != nil {
				failures = append(failures, fmt.Sprintf("%s: invalid symbol URI: %v", name, pathErr))
				continue
			}
			if path, pathErr = safeWorkspacePath(t.manager.root, path); pathErr != nil {
				failures = append(failures, fmt.Sprintf("%s: symbol URI is outside the workspace", name))
				continue
			}
			lines = append(lines, fmt.Sprintf("%s — %s:%d:%d", symbol.Name, displayWorkspacePath(t.manager.root, path), symbol.Location.Range.Start.Line+1, symbol.Location.Range.Start.Character+1))
			if len(lines) >= maxDiagnosticItems {
				break
			}
		}
		if len(lines) >= maxDiagnosticItems {
			break
		}
	}
	if len(lines) == 0 {
		if len(failures) > 0 {
			return tools.Result{Content: "Workspace symbol search failed: " + strings.Join(failures, "; "), IsError: true}, nil
		}
		return tools.Result{Content: "No workspace symbols found."}, nil
	}
	sort.Strings(lines)
	content := fmt.Sprintf("Found %d workspace symbol(s) matching %q:\n%s", len(lines), query, strings.Join(lines, "\n"))
	if len(lines) == maxDiagnosticItems {
		content += fmt.Sprintf("\n[truncated at %d symbols]", maxDiagnosticItems)
	}
	if len(failures) > 0 {
		content += "\nServer warnings: " + strings.Join(failures, "; ")
	}
	return tools.Result{Content: content}, nil
}

func (t *tool) rename(ctx context.Context, client *Client, params map[string]any, arguments map[string]any, toolContext tools.Context, file string) (tools.Result, error) {
	newName, ok := arguments["new_name"].(string)
	if !ok || strings.TrimSpace(newName) == "" {
		return tools.Result{}, errors.New("LSP rename requires a non-empty 'new_name'")
	}
	params["newName"] = strings.TrimSpace(newName)
	apply, present, err := optionalBool(arguments, "apply")
	if err != nil {
		return tools.Result{}, err
	}
	if !present {
		apply = true
	}
	if apply {
		if err := approveLSPMutation(ctx, toolContext, "rename symbol in "+file, "lsp:write"); err != nil {
			return tools.Result{}, err
		}
	}
	var edit workspaceEdit
	request := func() error { return client.Request(ctx, "textDocument/rename", params, &edit) }
	if apply {
		err = client.withWorkspaceEdits(request)
	} else {
		err = request()
	}
	if err != nil {
		return tools.Result{}, fmt.Errorf("LSP rename failed: %w", err)
	}
	if workspaceEditEmpty(edit) {
		return tools.Result{Content: "Rename returned no edits."}, nil
	}
	if !apply {
		return tools.Result{Content: "Rename preview:\n" + formatWorkspaceEdit(t.manager.root, edit)}, nil
	}
	if err := applyWorkspaceEdit(ctx, t.manager.root, edit, constrainedEditResolver(t.manager.root)); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: "Applied rename:\n" + formatWorkspaceEdit(t.manager.root, edit)}, nil
}

func (t *tool) renameFile(ctx context.Context, source string, arguments map[string]any, toolContext tools.Context, displaySource string) (tools.Result, error) {
	destinationValue, ok := arguments["new_name"].(string)
	if !ok || strings.TrimSpace(destinationValue) == "" {
		return tools.Result{}, errors.New("LSP rename_file requires a non-empty 'new_name'")
	}
	destination, err := toolContext.ResolvePath(destinationValue)
	if err != nil {
		return tools.Result{}, err
	}
	// LSP workspace edits and file notifications are scoped to the configured
	// project, even when the surrounding tool runs in full-access mode.
	if source, err = safeWorkspacePath(t.manager.root, source); err != nil {
		return tools.Result{}, err
	}
	if destination, err = safeWorkspacePath(t.manager.root, destination); err != nil {
		return tools.Result{}, err
	}
	if filepath.Clean(source) == filepath.Clean(t.manager.root) || filepath.Clean(destination) == filepath.Clean(t.manager.root) {
		return tools.Result{}, errors.New("LSP rename_file may not target the workspace root")
	}
	if filepath.Clean(source) == filepath.Clean(destination) {
		return tools.Result{}, errors.New("source and destination are identical")
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return tools.Result{}, fmt.Errorf("stat LSP rename source %s: %w", displaySource, err)
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return tools.Result{}, errors.New("LSP rename_file does not support symbolic-link sources")
	}
	if _, err := os.Lstat(destination); err == nil {
		return tools.Result{}, fmt.Errorf("destination already exists: %s", destinationValue)
	} else if !errors.Is(err, os.ErrNotExist) {
		return tools.Result{}, err
	}
	if sourceInfo.IsDir() && pathWithin(source, destination) {
		return tools.Result{}, errors.New("destination cannot be inside the source directory")
	}
	pairs, err := enumerateRenamePairs(source, destination, sourceInfo)
	if err != nil {
		return tools.Result{}, err
	}
	if len(pairs) == 0 {
		return tools.Result{}, errors.New("LSP rename_file source contains no regular files")
	}
	apply, present, err := optionalBool(arguments, "apply")
	if err != nil {
		return tools.Result{}, err
	}
	if !present {
		apply = true
	}
	if apply {
		if err := approveLSPMutation(ctx, toolContext, fmt.Sprintf("rename file %s to %s", displaySource, destinationValue), "lsp:write"); err != nil {
			return tools.Result{}, err
		}
	}
	fileParams := make([]map[string]string, 0, len(pairs))
	for _, pair := range pairs {
		fileParams = append(fileParams, map[string]string{"oldUri": fileURI(pair.oldPath), "newUri": fileURI(pair.newPath)})
	}
	// Ask every relevant server. A project may have a primary language server
	// plus one or more linters, and each can contribute distinct reference edits.
	clients := make(map[string]*Client)
	var serverNotes []string
	var hardFailures []string
	for _, pair := range pairs {
		for _, match := range append(t.manager.ServersForFile(pair.oldPath), t.manager.ServersForFile(pair.newPath)...) {
			if _, exists := clients[match.Name]; exists {
				continue
			}
			if match.Config.Linter != "" {
				serverNotes = append(serverNotes, fmt.Sprintf("  %s: CLI linter does not support file rename hooks", match.Name))
				continue
			}
			serverClient, _, clientErr := t.manager.Client(ctx, match.Name, match.Config)
			if clientErr != nil {
				serverNotes = append(serverNotes, fmt.Sprintf("  %s: %v", match.Name, clientErr))
				continue
			}
			clients[match.Name] = serverClient
		}
	}
	var edit workspaceEdit
	responding := make([]string, 0, len(clients))
	requestNames := make([]string, 0, len(clients))
	for name := range clients {
		requestNames = append(requestNames, name)
	}
	sort.Strings(requestNames)
	for _, name := range requestNames {
		client := clients[name]
		var candidate workspaceEdit
		request := func() error {
			return client.Request(ctx, "workspace/willRenameFiles", map[string]any{"files": fileParams}, &candidate)
		}
		var requestErr error
		if apply {
			requestErr = client.withWorkspaceEdits(request)
		} else {
			requestErr = request()
		}
		if requestErr != nil {
			if isMethodNotFound(requestErr) {
				continue
			}
			hardFailures = append(hardFailures, name)
			serverNotes = append(serverNotes, fmt.Sprintf("  %s: %v", name, requestErr))
			continue
		}
		responding = append(responding, name)
		mergeWorkspaceEdits(&edit, candidate)
	}
	sort.Strings(responding)
	preview := fmt.Sprintf("Rename preview: %s → %s", displaySource, destinationValue)
	if !apply {
		if !workspaceEditEmpty(edit) {
			preview += "\n" + formatWorkspaceEdit(t.manager.root, edit)
		}
		if len(serverNotes) > 0 {
			preview += "\nServer notes:\n" + strings.Join(serverNotes, "\n")
		}
		return tools.Result{Content: preview}, nil
	}
	if len(hardFailures) > 0 {
		return tools.Result{Content: fmt.Sprintf("Aborted rename: willRenameFiles failed on %s; no files were moved.\n%s", strings.Join(hardFailures, ", "), strings.Join(serverNotes, "\n")), IsError: true}, nil
	}
	if err := applyWorkspaceEditAndRename(ctx, t.manager.root, edit, constrainedEditResolver(t.manager.root), source, destination); err != nil {
		return tools.Result{}, err
	}
	clientNames := make([]string, 0, len(clients))
	for name := range clients {
		clientNames = append(clientNames, name)
	}
	sort.Strings(clientNames)
	for _, name := range clientNames {
		client := clients[name]
		for _, pair := range pairs {
			_ = client.CloseDocument(ctx, pair.oldPath)
		}
		_ = client.Notify("workspace/didRenameFiles", map[string]any{"files": fileParams})
	}
	return tools.Result{Content: fmt.Sprintf("Renamed %s → %s", displaySource, destinationValue)}, nil
}

func pathWithin(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (t *tool) codeActions(ctx context.Context, client *Client, params map[string]any, arguments map[string]any, toolContext tools.Context, file string) (tools.Result, error) {
	apply, present, err := optionalBool(arguments, "apply")
	if err != nil {
		return tools.Result{}, err
	}
	if !present {
		apply = false
	}
	selector := strings.TrimSpace(stringValue(arguments, "query"))
	if apply {
		// The request itself may trigger a server-initiated workspace/applyEdit,
		// so approval must happen before entering the mutation scope.
		if err := approveLSPMutation(ctx, toolContext, "apply code action in "+file, "lsp:write"); err != nil {
			return tools.Result{}, err
		}
	}
	textDocument, ok := params["textDocument"].(map[string]any)
	if !ok {
		return tools.Result{}, errors.New("LSP code_actions requires a textDocument parameter")
	}
	uri, ok := textDocument["uri"].(string)
	if !ok || uri == "" {
		return tools.Result{}, errors.New("LSP code_actions requires a document URI")
	}
	// Code-action requests require a range. The tool's line/symbol selector
	// identifies a point, so use a zero-width range at that point.
	position, _ := params["position"].(position)
	params["range"] = map[string]any{"start": position, "end": position}
	params["context"] = map[string]any{"diagnostics": client.Diagnostics(uri)}
	if !apply && isCodeActionKindSelector(selector) {
		params["context"].(map[string]any)["only"] = []string{selector}
	}
	var raw []json.RawMessage
	requestCodeActions := func() error { return client.Request(ctx, "textDocument/codeAction", params, &raw) }
	var requestErr error
	if apply {
		// Some servers answer a code-action request by first sending a
		// workspace/applyEdit request of their own.  Keep that request inside the
		// same approved mutation scope as the eventual edit/command.
		requestErr = client.withWorkspaceEdits(requestCodeActions)
	} else {
		requestErr = requestCodeActions()
	}
	if requestErr != nil {
		return tools.Result{}, fmt.Errorf("LSP code actions failed: %w", requestErr)
	}
	if len(raw) == 0 {
		return tools.Result{Content: "No code actions available."}, nil
	}
	if !apply {
		var lines []string
		for index, item := range raw {
			var action codeAction
			if json.Unmarshal(item, &action) == nil && action.Title != "" {
				lines = append(lines, fmt.Sprintf("%d: %s", index, action.Title))
				continue
			}
			var command command
			if json.Unmarshal(item, &command) == nil {
				lines = append(lines, fmt.Sprintf("%d: %s", index, command.Title))
			}
		}
		return tools.Result{Content: fmt.Sprintf("%d code action(s):\n%s", len(lines), strings.Join(lines, "\n"))}, nil
	}
	index := -1
	if selector != "" {
		if parsed, parseErr := strconv.Atoi(selector); parseErr == nil && parsed >= 0 && parsed < len(raw) {
			index = parsed
		} else {
			for itemIndex, item := range raw {
				var action codeAction
				if json.Unmarshal(item, &action) == nil && strings.Contains(strings.ToLower(action.Title), strings.ToLower(selector)) {
					index = itemIndex
					break
				}
			}
		}
	}
	if index < 0 {
		return tools.Result{Content: "Specify query as a code-action index or title when apply=true.", IsError: true}, nil
	}
	var action codeAction
	if err := json.Unmarshal(raw[index], &action); err == nil && action.Title != "" {
		if len(action.Data) > 0 && action.Edit == nil && action.Command == nil {
			resolve := func() error { return client.Request(ctx, "codeAction/resolve", action, &action) }
			var resolveErr error
			if apply {
				resolveErr = client.withWorkspaceEdits(resolve)
			} else {
				resolveErr = resolve()
			}
			if resolveErr != nil {
				return tools.Result{}, fmt.Errorf("resolve LSP code action: %w", resolveErr)
			}
		}
		changed := false
		if action.Edit != nil {
			if err := applyWorkspaceEdit(ctx, t.manager.root, *action.Edit, constrainedEditResolver(t.manager.root)); err != nil {
				return tools.Result{}, err
			}
			changed = true
		}
		if action.Command != nil {
			if err := executeLSPCommand(ctx, client, *action.Command); err != nil {
				return tools.Result{}, err
			}
			changed = true
		}
		if !changed {
			return tools.Result{Content: fmt.Sprintf("Code action %q has no edit or command to apply.", action.Title), IsError: true}, nil
		}
		return tools.Result{Content: fmt.Sprintf("Applied code action: %s", action.Title)}, nil
	}
	var command command
	if err := json.Unmarshal(raw[index], &command); err != nil || command.Command == "" {
		return tools.Result{}, errors.New("selected LSP code action has an invalid response shape")
	}
	if err := executeLSPCommand(ctx, client, command); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: fmt.Sprintf("Executed code action command: %s", command.Title)}, nil
}

func executeLSPCommand(ctx context.Context, client *Client, command command) error {
	params := map[string]any{"command": command.Command}
	if command.Arguments != nil {
		params["arguments"] = command.Arguments
	}
	var ignored json.RawMessage
	if err := client.withWorkspaceEdits(func() error {
		return client.Request(ctx, "workspace/executeCommand", params, &ignored)
	}); err != nil {
		return fmt.Errorf("execute LSP command %s: %w", command.Command, err)
	}
	return nil
}

func (t *tool) formatting(ctx context.Context, client *Client, uri string, arguments map[string]any, toolContext tools.Context, file string) (tools.Result, error) {
	params := map[string]any{"textDocument": map[string]any{"uri": uri}, "options": map[string]any{"tabSize": 4, "insertSpaces": true}}
	apply, present, err := optionalBool(arguments, "apply")
	if err != nil {
		return tools.Result{}, err
	}
	if !present {
		apply = true
	}
	if apply {
		if err := approveLSPMutation(ctx, toolContext, "format "+file, "lsp:write"); err != nil {
			return tools.Result{}, err
		}
	}
	var edits []textEdit
	request := func() error { return client.Request(ctx, "textDocument/formatting", params, &edits) }
	if apply {
		err = client.withWorkspaceEdits(request)
	} else {
		err = request()
	}
	if err != nil {
		return tools.Result{}, fmt.Errorf("LSP formatting failed: %w", err)
	}
	if len(edits) == 0 {
		return tools.Result{Content: "No formatting edits returned."}, nil
	}
	edit := workspaceEdit{Changes: map[string][]textEdit{uri: edits}}
	if !apply {
		return tools.Result{Content: "Formatting preview:\n" + formatWorkspaceEdit(t.manager.root, edit)}, nil
	}
	if err := applyWorkspaceEdit(ctx, t.manager.root, edit, constrainedEditResolver(t.manager.root)); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: fmt.Sprintf("Formatted %s", file)}, nil
}

func (t *tool) linterFormatting(ctx context.Context, name string, server ServerConfig, pathValue string, content []byte, arguments map[string]any, toolContext tools.Context, displayFile string) (tools.Result, error) {
	apply, present, err := optionalBool(arguments, "apply")
	if err != nil {
		return tools.Result{}, err
	}
	if !present {
		apply = true
	}
	if apply {
		if err := approveLSPMutation(ctx, toolContext, "format "+displayFile, "lsp:write"); err != nil {
			return tools.Result{}, err
		}
	}
	formatted, err := t.manager.LinterFormat(ctx, name, server, pathValue, string(content))
	if err != nil {
		return tools.Result{}, fmt.Errorf("CLI formatter %s failed: %w", name, err)
	}
	if formatted == string(content) {
		return tools.Result{Content: "No formatting edits returned."}, nil
	}
	uri := fileURI(pathValue)
	edit := workspaceEdit{Changes: map[string][]textEdit{uri: {{
		Range:   fullDocumentRange(string(content)),
		NewText: formatted,
	}}}}
	if !apply {
		return tools.Result{Content: "Formatting preview:\n" + formatWorkspaceEdit(t.manager.root, edit)}, nil
	}
	if err := applyWorkspaceEdit(ctx, t.manager.root, edit, constrainedEditResolver(t.manager.root)); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: fmt.Sprintf("Formatted %s", displayFile)}, nil
}

func fullDocumentRange(content string) lspRange {
	lines := strings.Split(content, "\n")
	lastLine := len(lines) - 1
	last := strings.TrimSuffix(lines[lastLine], "\r")
	return lspRange{Start: position{Line: 0, Character: 0}, End: position{Line: lastLine, Character: utf16Column(last, len(last))}}
}

func (t *tool) rawRequest(ctx context.Context, arguments map[string]any, toolContext tools.Context) (tools.Result, error) {
	requestContext, cancel := context.WithTimeout(ctx, lspTimeout(arguments))
	defer cancel()
	method := strings.TrimSpace(stringValue(arguments, "query"))
	if method == "" {
		return tools.Result{}, errors.New("LSP request requires 'query' as the method name")
	}
	if strings.Contains(method, " ") || strings.Contains(method, "\n") {
		return tools.Result{}, errors.New("LSP request method must not contain whitespace")
	}
	fileValue := strings.TrimSpace(stringValue(arguments, "file"))
	var client *Client
	var err error
	var params any = map[string]any{}
	payloadProvided := strings.TrimSpace(stringValue(arguments, "payload")) != ""
	if payload := strings.TrimSpace(stringValue(arguments, "payload")); payload != "" {
		if err := json.Unmarshal([]byte(payload), &params); err != nil {
			return tools.Result{}, fmt.Errorf("LSP request payload is not valid JSON: %w", err)
		}
	}
	if fileValue != "" && fileValue != "*" {
		pathValue, resolveErr := toolContext.ResolvePath(fileValue)
		if resolveErr != nil {
			return tools.Result{}, resolveErr
		}
		if pathValue, resolveErr = safeWorkspacePath(t.manager.root, pathValue); resolveErr != nil {
			return tools.Result{}, resolveErr
		}
		serverName, serverConfig, serverErr := t.manager.ServerForFile(pathValue)
		if serverErr != nil {
			return tools.Result{}, serverErr
		}
		if serverConfig.Linter != "" {
			return tools.Result{}, fmt.Errorf("LSP server %q is a CLI linter and does not support raw JSON-RPC requests", serverName)
		}
		client, _, err = t.manager.Client(requestContext, serverName, serverConfig)
		if err != nil {
			return tools.Result{}, err
		}
		content, readErr := os.ReadFile(pathValue)
		if readErr != nil {
			return tools.Result{}, readErr
		}
		uri, openErr := client.OpenDocument(requestContext, pathValue, string(content))
		if openErr != nil {
			return tools.Result{}, openErr
		}
		if !payloadProvided {
			// Auto-built parameters are intentionally added only when no payload was
			// supplied. A caller-provided JSON payload is authoritative and must be
			// passed through verbatim (matching the LSP tool contract).
			if _, isObject := params.(map[string]any); isObject {
				values := params.(map[string]any)
				if _, exists := values["textDocument"]; !exists {
					values["textDocument"] = map[string]any{"uri": uri}
				}
			}
		}
	} else {
		config := t.manager.Config()
		for _, name := range config.Names() {
			if config.Servers[name].Linter != "" {
				continue
			}
			client, _, err = t.manager.Client(requestContext, name, config.Servers[name])
			if err == nil {
				break
			}
		}
		if client == nil {
			return tools.Result{}, errors.New("no available language server for raw request")
		}
	}
	// Raw methods are intentionally outside the typed, read-only action list.
	// A custom method can execute commands or mutate server/workspace state
	// without containing an obvious word such as "rename", so every raw request
	// uses the write-tier approval boundary.
	if err := approveLSPMutation(requestContext, toolContext, "raw LSP request "+method, "lsp:request"); err != nil {
		return tools.Result{}, err
	}
	var result json.RawMessage
	request := func() error { return client.Request(requestContext, method, params, &result) }
	err = client.withWorkspaceEdits(request)
	if err != nil {
		return tools.Result{}, fmt.Errorf("LSP request %s failed: %w", method, err)
	}
	if len(result) == 0 {
		result = []byte("null")
	}
	var pretty any
	if json.Unmarshal(result, &pretty) == nil {
		encoded, _ := json.MarshalIndent(pretty, "", "  ")
		return tools.Result{Content: string(encoded)}, nil
	}
	return tools.Result{Content: string(result)}, nil
}

// withWorkspaceContext supplies the manager's workspace when an embedding
// host omits it from tools.Context. LSP remains workspace-scoped even in
// full-access mode, so this fallback only fills in the missing root and does
// not widen any path permissions.
func (t *tool) withWorkspaceContext(toolContext tools.Context) tools.Context {
	if strings.TrimSpace(toolContext.WorkspaceRoot) == "" && t != nil && t.manager != nil {
		toolContext.WorkspaceRoot = t.manager.root
	}
	return toolContext
}

func (t *tool) selectedServerNames(arguments map[string]any, toolContext tools.Context) []string {
	file := strings.TrimSpace(stringValue(arguments, "file"))
	if file == "" || file == "*" {
		return t.manager.Config().Names()
	}
	pathValue, err := toolContext.ResolvePath(file)
	if err != nil {
		return nil
	}
	if pathValue, err = safeWorkspacePath(t.manager.root, pathValue); err != nil {
		return nil
	}
	matches := t.manager.ServersForFile(pathValue)
	if len(matches) == 0 {
		return nil
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match.Name)
	}
	return names
}

func lspTimeout(arguments map[string]any) time.Duration {
	if value, ok := numericValue(arguments["timeout_ms"]); ok && value >= 1000 {
		if value > 300000 {
			value = 300000
		}
		return time.Duration(value) * time.Millisecond
	}
	if value, ok := numericValue(arguments["timeout"]); ok && value > 0 {
		if value > 300 {
			value = 300
		}
		return time.Duration(value * float64(time.Second))
	}
	return defaultRequestTimeout
}

func optionalLine(arguments map[string]any) (int, error) {
	value, exists := arguments["line"]
	if !exists {
		return 1, nil
	}
	number, ok := numericValue(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number != float64(int(number)) || number < 1 {
		return 0, errors.New("LSP 'line' must be a positive integer")
	}
	return int(number), nil
}

func optionalBool(arguments map[string]any, key string) (bool, bool, error) {
	value, exists := arguments[key]
	if !exists {
		return false, false, nil
	}
	result, ok := value.(bool)
	if !ok {
		return false, true, fmt.Errorf("LSP %q must be a boolean", key)
	}
	return result, true, nil
}

func stringValue(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return value
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func isNonNegativeInteger(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func isCodeActionKindSelector(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || isNonNegativeInteger(value) {
		return false
	}
	for _, prefix := range []string{"quickfix", "refactor", "source", "organizeimports", "fixall", "inline"} {
		if value == prefix || strings.HasPrefix(value, prefix+".") {
			return true
		}
	}
	return false
}

func isHierarchicalSymbolResponse(raw json.RawMessage) bool {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return false
	}
	if len(values) == 0 {
		return true
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(values[0], &object); err != nil {
		return false
	}
	_, hasSelectionRange := object["selectionRange"]
	return hasSelectionRange
}

func resolveSymbolColumn(content string, line int, symbol string) (int, error) {
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return 0, fmt.Errorf("LSP line %d is outside the document", line)
	}
	value := strings.TrimSuffix(lines[line-1], "\r")
	if symbol == "" {
		for index, runeValue := range value {
			if !isWhitespace(runeValue) {
				return utf16Column(value, index), nil
			}
		}
		return 0, nil
	}
	occurrence := 1
	if hash := strings.LastIndex(symbol, "#"); hash >= 0 && hash+1 < len(symbol) {
		if parsed, err := strconv.Atoi(symbol[hash+1:]); err == nil && parsed > 0 {
			occurrence = parsed
			symbol = symbol[:hash]
		}
	}
	if symbol == "" {
		return 0, errors.New("LSP symbol selector is empty")
	}
	start := 0
	for count := 0; ; count++ {
		index := strings.Index(value[start:], symbol)
		if index < 0 {
			break
		}
		index += start
		if count+1 == occurrence {
			return utf16Column(value, index), nil
		}
		start = index + len(symbol)
	}
	// Case-insensitive fallback is useful for servers that normalize symbols.
	lowerValue, lowerSymbol := strings.ToLower(value), strings.ToLower(symbol)
	start = 0
	for count := 0; ; count++ {
		index := strings.Index(lowerValue[start:], lowerSymbol)
		if index < 0 {
			break
		}
		index += start
		if count+1 == occurrence {
			return utf16Column(value, index), nil
		}
		start = index + len(lowerSymbol)
	}
	return 0, fmt.Errorf("symbol %q was not found on line %d", symbol, line)
}

func utf16Column(value string, byteIndex int) int {
	column := 0
	for _, runeValue := range value[:byteIndex] {
		if runeValue > 0xffff {
			column += 2
		} else {
			column++
		}
	}
	return column
}

func isWhitespace(value rune) bool { return value == ' ' || value == '\t' || value == '\r' }

func decodeLocations(raw json.RawMessage) ([]location, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err == nil {
		result := make([]location, 0, len(items))
		for _, item := range items {
			decoded, decodeErr := decodeLocation(item)
			if decodeErr != nil {
				return nil, decodeErr
			}
			result = append(result, decoded)
		}
		return result, nil
	}
	decoded, err := decodeLocation(raw)
	if err == nil {
		return []location{decoded}, nil
	}
	return nil, fmt.Errorf("decode LSP locations: invalid response")
}

func decodeLocation(raw json.RawMessage) (location, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return location{}, err
	}
	if keys["uri"] != nil {
		var value location
		if err := json.Unmarshal(raw, &value); err != nil || value.URI == "" {
			return location{}, errors.New("LSP location has an empty URI")
		}
		return value, nil
	}
	if keys["targetUri"] != nil {
		var link locationLink
		if err := json.Unmarshal(raw, &link); err != nil || link.TargetURI == "" {
			return location{}, errors.New("LSP location link has an empty target URI")
		}
		rangeValue := link.TargetRange
		if link.TargetSelectionRange != nil {
			rangeValue = *link.TargetSelectionRange
		}
		return location{URI: link.TargetURI, Range: rangeValue}, nil
	}
	return location{}, errors.New("LSP location response has neither uri nor targetUri")
}

func extractHoverText(raw json.RawMessage) string {
	var value struct {
		Contents json.RawMessage `json:"contents"`
	}
	if json.Unmarshal(raw, &value) != nil || len(value.Contents) == 0 {
		return ""
	}
	return flattenMarkup(value.Contents)
}

func flattenMarkup(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) == nil {
		parts := make([]string, 0, len(values))
		for _, item := range values {
			parts = append(parts, flattenMarkup(item))
		}
		return strings.Join(parts, "\n")
	}
	var object struct {
		Value    string `json:"value"`
		Language string `json:"language"`
	}
	if json.Unmarshal(raw, &object) == nil {
		if object.Value != "" {
			if object.Language != "" {
				return "```" + object.Language + "\n" + object.Value + "\n```"
			}
			return object.Value
		}
	}
	return ""
}

func appendDocumentSymbolLines(lines *[]string, symbol documentSymbol, depth int) {
	*lines = append(*lines, fmt.Sprintf("%s%s @ %d:%d", strings.Repeat("  ", depth), symbol.Name, symbol.SelectionRange.Start.Line+1, symbol.SelectionRange.Start.Character+1))
	for _, child := range symbol.Children {
		appendDocumentSymbolLines(lines, child, depth+1)
	}
}

func diagnosticSeverity(value int) string {
	switch value {
	case 1:
		return "ERROR"
	case 2:
		return "WARN"
	case 3:
		return "INFO"
	case 4:
		return "HINT"
	default:
		return "INFO"
	}
}

func hasErrorDiagnostic(values []diagnostic) bool {
	for _, value := range values {
		if value.Severity == 1 {
			return true
		}
	}
	return false
}

func capabilityEnabled(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case map[string]any:
		// LSP capability objects are presence-based; an empty object still means
		// the server implements the feature.
		return true
	default:
		return value != nil
	}
}

func navigationLabel(action string) string {
	switch action {
	case "definition":
		return "definition(s)"
	case "type_definition":
		return "type definition(s)"
	case "implementation":
		return "implementation(s)"
	case "references":
		return "reference(s)"
	default:
		return action
	}
}

func workspaceEditEmpty(edit workspaceEdit) bool {
	if len(edit.Changes) > 0 {
		for _, values := range edit.Changes {
			if len(values) > 0 {
				return false
			}
		}
	}
	return len(edit.DocumentChanges) == 0
}

func formatWorkspaceEdit(root string, edit workspaceEdit) string {
	var lines []string
	paths := make([]string, 0, len(edit.Changes))
	for uri := range edit.Changes {
		paths = append(paths, uri)
	}
	sort.Strings(paths)
	for _, uri := range paths {
		pathValue, _ := uriToPath(uri)
		pathValue = displayWorkspacePath(root, pathValue)
		lines = append(lines, fmt.Sprintf("%s (%d edit(s))", pathValue, len(edit.Changes[uri])))
	}
	if len(edit.DocumentChanges) > 0 {
		lines = append(lines, fmt.Sprintf("documentChanges (%d operation(s))", len(edit.DocumentChanges)))
		for _, raw := range edit.DocumentChanges {
			var operation resourceOperation
			if json.Unmarshal(raw, &operation) != nil || operation.Kind == "" {
				continue
			}
			switch operation.Kind {
			case "create", "delete":
				pathValue, pathErr := uriToPath(operation.URI)
				if pathErr == nil {
					lines = append(lines, fmt.Sprintf("  %s %s", operation.Kind, displayWorkspacePath(root, pathValue)))
				}
			case "rename":
				oldPath, oldErr := uriToPath(operation.OldURI)
				newPath, newErr := uriToPath(operation.NewURI)
				if oldErr == nil && newErr == nil {
					lines = append(lines, fmt.Sprintf("  rename %s → %s", displayWorkspacePath(root, oldPath), displayWorkspacePath(root, newPath)))
				}
			}
		}
	}
	if len(lines) == 0 {
		return "(no edits)"
	}
	return strings.Join(lines, "\n")
}

func displayWorkspacePath(root string, value string) string {
	if relative, err := filepath.Rel(root, value); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return filepath.ToSlash(value)
}

func truncateLSP(value string) string {
	if len(value) <= maxLSPResultBytes {
		return value
	}
	value = value[:maxLSPResultBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "\n\n[truncated]"
}

func jsonTextResult(value any) (tools.Result, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: truncateLSP(string(encoded))}, nil
}

func approveLSPMutation(ctx context.Context, toolContext tools.Context, action string, scope string) error {
	allowed, err := toolContext.RequestPermission(ctx, tools.ApprovalRequest{ToolName: "LSP", Kind: tools.ApprovalHost, Scope: scope, Command: "LSP: " + action, Justification: "语言服务器操作可能修改工作区或执行服务器命令"})
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("User denied LSP mutation")
	}
	return nil
}

func isMethodNotFound(err error) bool {
	if err == nil {
		return false
	}
	var protocolError *rpcError
	if errors.As(err, &protocolError) && protocolError.Code == -32601 {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "method not found") || strings.Contains(message, "method not implemented")
}
