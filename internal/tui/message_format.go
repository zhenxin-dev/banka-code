package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/zhenxin-dev/banka-code/internal/messages"
)

type builtinCommand struct {
	Name        string
	Command     string
	Description string
}

var builtinCommands = []builtinCommand{
	{Name: "help", Command: "/help", Description: "查看内置命令帮助"},
	{Name: "clear", Command: "/clear", Description: "清空当前会话内容"},
	{Name: "undo", Command: "/undo", Description: "回滚上一轮会话上下文（不修改文件）"},
	{Name: "compact", Command: "/compact", Description: "压缩较早的会话上下文"},
	{Name: "status", Command: "/status", Description: "查看当前会话状态"},
	{Name: "exit", Command: "/exit", Description: "退出 Banka Code"},
	{Name: "quit", Command: "/quit", Description: "退出 Banka Code"},
}

func parseBuiltinCommand(value string) (builtinCommand, bool) {
	prompt := strings.ToLower(strings.TrimSpace(value))
	for _, command := range builtinCommands {
		if command.Command == prompt {
			return command, true
		}
	}
	return builtinCommand{}, false
}

func findBuiltinCommands(value string) []builtinCommand {
	prompt := strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(prompt, "/") {
		return nil
	}
	result := make([]builtinCommand, 0, len(builtinCommands))
	for _, command := range builtinCommands {
		if prompt == "/" || strings.HasPrefix(command.Command, prompt) {
			result = append(result, command)
		}
	}
	return result
}

func normalizeCommandSelection(index int, count int) int {
	if count <= 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= count {
		return count - 1
	}
	return index
}

func moveCommandSelection(index int, count int, direction int) int {
	if count <= 0 {
		return 0
	}
	index = normalizeCommandSelection(index, count)
	return (index + direction + count) % count
}

func horizontalRule(width int, character string) string {
	if width < 1 {
		width = 1
	}
	return strings.Repeat(character, width)
}

func titledRule(width int, title string, character string) string {
	if width < 1 {
		width = 1
	}
	title = strings.TrimSpace(title)
	if title == "" || width <= utf8.RuneCountInString(title)+2 {
		return horizontalRule(width, character)
	}
	decorated := " " + title + " "
	sideWidth := width - utf8.RuneCountInString(decorated)
	left := sideWidth / 2
	return strings.Repeat(character, left) + decorated + strings.Repeat(character, sideWidth-left)
}

func buildBuiltinHelpBody() string {
	lines := []string{"## 可用命令", ""}
	for _, command := range builtinCommands {
		lines = append(lines, fmt.Sprintf("- `%s`：%s", command.Command, command.Description))
	}
	return strings.Join(lines, "\n")
}

func formatToolCall(call messages.ToolCall) string {
	summary := summarizeToolArguments(call.Name, call.ArgumentsJSON)
	if summary == "" {
		return toolDisplayName(call.Name)
	}
	return toolDisplayName(call.Name) + " · " + summary
}

func toolDisplayName(name string) string {
	switch name {
	case "Bash", "Read", "Write", "Edit", "Glob", "Grep", "WebFetch", "ApplyPatch", "AskUser", "Skill",
		"MCPListResources", "MCPReadResource", "MCPListPrompts", "MCPGetPrompt":
		return name
	default:
		parts := strings.Fields(strings.ReplaceAll(name, "_", " "))
		for index, part := range parts {
			runes := []rune(part)
			if len(runes) > 0 {
				runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
				parts[index] = string(runes)
			}
		}
		return strings.Join(parts, " ")
	}
}

func summarizeToolArguments(toolName string, argumentsJSON string) string {
	var arguments map[string]any
	if json.Unmarshal([]byte(argumentsJSON), &arguments) != nil {
		return ""
	}
	switch toolName {
	case "Bash":
		return readTrimmedString(arguments, "command", 72)
	case "Read", "Write", "Edit":
		return readTrimmedString(arguments, "path", 72)
	case "Glob":
		return readTrimmedString(arguments, "pattern", 72)
	case "Grep":
		pattern := readTrimmedString(arguments, "pattern", 44)
		include := readTrimmedString(arguments, "include", 24)
		if pattern != "" && include != "" {
			return pattern + " in " + include
		}
		return pattern + include
	case "WebFetch":
		return readTrimmedString(arguments, "url", 72)
	case "AskUser":
		return readTrimmedString(arguments, "question", 72)
	case "Skill":
		return readTrimmedString(arguments, "name", 48)
	case "MCPReadResource":
		return readTrimmedString(arguments, "uri", 72)
	case "MCPGetPrompt":
		return readTrimmedString(arguments, "name", 48)
	case "ApplyPatch":
		return "unified diff"
	default:
		return ""
	}
}

func readTrimmedString(arguments map[string]any, key string, maxLength int) string {
	value, ok := arguments[key].(string)
	if !ok {
		return ""
	}
	normalized := strings.Join(strings.Fields(value), " ")
	runes := []rune(normalized)
	if len(runes) <= maxLength {
		return normalized
	}
	return string(runes[:maxLength-1]) + "…"
}
