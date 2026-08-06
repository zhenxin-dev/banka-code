package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/zhenxin-dev/banka-code/internal/agent"
	"github.com/zhenxin-dev/banka-code/internal/config"
	"github.com/zhenxin-dev/banka-code/internal/messages"
	"github.com/zhenxin-dev/banka-code/internal/permissions"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

func TestBuiltinCommandsUpdateSessionState(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{
		Provider: config.ProviderAnthropic,
		Model:    "claude-test",
	}, nil, tools.NewRegistry(nil), tools.Context{})
	model.entries = []uiEntry{{kind: "user", body: "hello"}}
	model.transcript = []messages.Message{messages.NewUserMessage("hello")}

	model.submit("/status")
	if len(model.entries) != 2 || !strings.Contains(model.entries[1].body, "Anthropic") || !strings.Contains(model.entries[1].body, "claude-test") {
		t.Fatalf("status did not report runtime state: %#v", model.entries)
	}

	model.submit("/clear")
	if len(model.entries) != 0 || len(model.transcript) != 0 {
		t.Fatalf("clear did not reset the session: entries=%#v transcript=%#v", model.entries, model.transcript)
	}

	model.submit("/help")
	if len(model.entries) != 1 || !strings.Contains(model.entries[0].body, "/status") {
		t.Fatalf("help did not list commands: %#v", model.entries)
	}
}

func TestUndoRestoresPreviousConversationCheckpoint(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.entries = []uiEntry{{kind: "user", body: "first"}, {kind: "assistant", body: "reply"}}
	model.transcript = []messages.Message{
		messages.NewUserMessage("first"), messages.NewAssistantMessage("reply", nil),
	}
	model.saveCheckpoint()
	model.entries = append(model.entries, uiEntry{kind: "user", body: "second"})
	model.transcript = append(model.transcript, messages.NewUserMessage("second"))

	model.submit("/undo")
	if len(model.transcript) != 2 || model.transcript[0].Content != "first" {
		t.Fatalf("transcript was not restored: %#v", model.transcript)
	}
	if len(model.checkpoints) != 0 || strings.Contains(model.entries[len(model.entries)-2].body, "second") {
		t.Fatalf("checkpoint was not consumed cleanly: entries=%#v checkpoints=%#v", model.entries, model.checkpoints)
	}
}

func TestStreamingEventsCreateAssistantAndToolEntries(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.busy = true
	model.entries = []uiEntry{{kind: "user", body: "prompt"}, {kind: "assistant"}}
	model.streamingEntry = 1

	model.Update(agentTextDeltaMsg("thinking"))
	model.Update(agentToolCallMsg(messages.ToolCall{Name: "Read", ArgumentsJSON: `{"path":"README.md"}`}))
	model.Update(agentTextDeltaMsg("done"))
	model.Update(agentFinishedMsg{result: agent.RunResult{
		FinalText: "done", Iterations: 2,
		Transcript: []messages.Message{messages.NewUserMessage("prompt")},
	}})

	if model.busy || model.streamingEntry != -1 {
		t.Fatalf("turn did not finish: busy=%v streaming=%d", model.busy, model.streamingEntry)
	}
	if len(model.entries) != 5 {
		t.Fatalf("unexpected entries: %#v", model.entries)
	}
	if model.entries[1].kind != "assistant" || model.entries[1].body != "thinking" {
		t.Fatalf("unexpected first assistant entry: %#v", model.entries[1])
	}
	if model.entries[2].kind != "tool" || model.entries[2].body != "Read · README.md" {
		t.Fatalf("unexpected tool entry: %#v", model.entries[2])
	}
	if model.entries[3].body != "done" || model.entries[4].kind != "status" {
		t.Fatalf("unexpected final entries: %#v", model.entries[3:])
	}
}

func TestApprovalRequestPausesAndResumesAgent(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.busy = true
	response := make(chan interactionResponse, 1)
	model.Update(approvalRequestMsg{
		request: tools.ApprovalRequest{
			ToolName: "Bash", Command: "curl https://example.com", Justification: "读取文档",
		},
		response: response,
	})
	if model.pending == nil || model.pending.approval == nil {
		t.Fatal("approval request did not enter pending state")
	}
	model.handleInteractionKey(tea.KeyPressMsg{Code: tea.KeyDown})
	model.handleInteractionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	result := <-response
	if result.decision != tools.ApprovalAllowAlways || model.pending != nil {
		t.Fatalf("approval was not resolved: result=%#v pending=%#v", result, model.pending)
	}
}

func TestPermissionsCommandSelectsYOLOMode(t *testing.T) {
	policy := permissions.NewPolicy(permissions.ModeDefault)
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{Permissions: policy})

	model.submit("/permissions")
	if model.pending == nil || !model.pending.permissions {
		t.Fatal("permissions command did not open the mode selector")
	}
	model.handleInteractionKey(tea.KeyPressMsg{Code: tea.KeyDown})
	model.handleInteractionKey(tea.KeyPressMsg{Code: tea.KeyDown})
	model.handleInteractionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if policy.Mode() != permissions.ModeYOLO || model.pending != nil {
		t.Fatalf("permission mode was not updated: mode=%q pending=%#v", policy.Mode(), model.pending)
	}
}

func TestPermissionSelectorFitsNarrowTerminal(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
	model.submit("/permissions")
	view := model.View()

	if height := lipgloss.Height(view.Content); height > 16 {
		t.Fatalf("permission selector height %d exceeds terminal", height)
	}
	for _, line := range strings.Split(view.Content, "\n") {
		if width := lipgloss.Width(line); width > 40 {
			t.Fatalf("permission selector width %d exceeds terminal: %q", width, line)
		}
	}
}

func TestQuestionRequestMapsNumberedOption(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.busy = true
	response := make(chan interactionResponse, 1)
	model.Update(questionRequestMsg{
		request:  tools.QuestionRequest{Question: "选择方案", Options: []string{"A", "B"}},
		response: response,
	})
	model.submitInteraction("2")
	result := <-response
	if result.answer != "B" {
		t.Fatalf("got answer %q, want B", result.answer)
	}
	if len(model.entries) == 0 || model.entries[len(model.entries)-1].body != "B" {
		t.Fatalf("answer was not shown in transcript: %#v", model.entries)
	}
}

func TestEscapeCancelsPendingQuestionAndKeepsConsumingEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	model := newAppModel(ctx, "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.busy = true
	model.cancel = cancel
	model.pending = &pendingInteraction{question: &tools.QuestionRequest{Question: "continue?"}, response: make(chan interactionResponse, 1)}
	_, command := model.handleInteractionKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command == nil || model.pending != nil || ctx.Err() == nil {
		t.Fatalf("pending question was not canceled cleanly: pending=%#v err=%v", model.pending, ctx.Err())
	}
}

func TestCanceledTurnKeepsPartialTextWithoutError(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.busy = true
	model.entries = []uiEntry{{kind: "assistant", body: "partial"}}
	model.streamingEntry = 0

	model.Update(agentFinishedMsg{err: context.Canceled})
	if model.busy || len(model.entries) != 1 || model.entries[0].body != "partial" {
		t.Fatalf("canceled turn lost partial output: %#v", model.entries)
	}
}

func TestFailedTurnShowsError(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.busy = true
	model.entries = []uiEntry{{kind: "assistant"}}
	model.streamingEntry = 0

	model.Update(agentFinishedMsg{err: errors.New("boom")})
	if len(model.entries) != 1 || model.entries[0].kind != "error" || model.entries[0].body != "boom" {
		t.Fatalf("failed turn did not show error: %#v", model.entries)
	}
}

func TestViewFitsTerminalWithCommandPanel(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{
		Provider: config.ProviderOpenAIChat,
		Model:    "test-model",
	}, nil, tools.NewRegistry(nil), tools.Context{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model.input.SetValue("/")
	model.resize()
	view := model.View()

	if height := lipgloss.Height(view.Content); height > 24 {
		t.Fatalf("view height %d exceeds terminal height", height)
	}
	for _, line := range strings.Split(view.Content, "\n") {
		if width := lipgloss.Width(line); width > 80 {
			t.Fatalf("line width %d exceeds terminal width: %q", width, line)
		}
	}
}

func TestExtractVisibleSelectionHandlesMultipleLinesAndWideText(t *testing.T) {
	view := "first line\n你好 Banka\nlast line"
	selected := extractVisibleSelection(view, screenPosition{x: 6, y: 0}, screenPosition{x: 3, y: 1})
	if selected != "line\n你好" {
		t.Fatalf("got selection %q, want %q", selected, "line\n你好")
	}
}
