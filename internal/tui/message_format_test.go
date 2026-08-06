package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zhenxin-dev/banka-code/internal/messages"
)

func TestBuiltinCommandParsingAndSuggestions(t *testing.T) {
	command, ok := parseBuiltinCommand(" /STATUS ")
	if !ok || command.Name != "status" {
		t.Fatalf("unexpected parsed command: %#v, %v", command, ok)
	}
	if _, ok := parseBuiltinCommand("hello"); ok {
		t.Fatalf("ordinary text was parsed as a command")
	}
	suggestions := findBuiltinCommands("/st")
	if len(suggestions) != 1 || suggestions[0].Command != "/status" {
		t.Fatalf("unexpected suggestions: %#v", suggestions)
	}
	all := findBuiltinCommands("/")
	if len(all) != len(builtinCommands) {
		t.Fatalf("got %d commands, want %d", len(all), len(builtinCommands))
	}
}

func TestCommandSelectionWraps(t *testing.T) {
	got := []int{
		normalizeCommandSelection(-1, 5),
		normalizeCommandSelection(99, 3),
		moveCommandSelection(0, 3, -1),
		moveCommandSelection(2, 3, 1),
	}
	if !reflect.DeepEqual(got, []int{0, 2, 2, 0}) {
		t.Fatalf("unexpected selection results: %#v", got)
	}
}

func TestTitledRulePreservesWidth(t *testing.T) {
	rule := titledRule(20, "chat", "─")
	if !strings.Contains(rule, " chat ") || len([]rune(rule)) != 20 {
		t.Fatalf("unexpected rule: %q", rule)
	}
}

func TestFormatToolCallSummarizesArguments(t *testing.T) {
	call := messages.ToolCall{Name: "Grep", ArgumentsJSON: `{"pattern":"Banka","include":"**/*.go"}`}
	if got := formatToolCall(call); got != "Grep · Banka in **/*.go" {
		t.Fatalf("got %q", got)
	}
}
