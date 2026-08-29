package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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

func TestSkillSlashCommandInvokesRuntimeHandler(t *testing.T) {
	called := false
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.details.Actions.SkillNames = []string{"review"}
	model.details.Actions.InvokeSkill = func(_ context.Context, name string, args string) (string, error) {
		called = true
		if name != "review" || args != "changed files" {
			t.Fatalf("unexpected skill invocation: %q %q", name, args)
		}
		return "check the diff", nil
	}
	model.submit("/skill:review changed files")
	if !called || !model.busy || len(model.entries) < 2 || model.entries[len(model.entries)-2].body != "/skill:review changed files" {
		t.Fatalf("skill command did not start a turn: called=%v busy=%v entries=%#v", called, model.busy, model.entries)
	}
}

func TestManagedCommandsUseRuntimeHandler(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.details.Actions.MCP = func(_ context.Context, command string) (string, error) {
		if command != "tools" {
			t.Fatalf("unexpected MCP command: %q", command)
		}
		return "mcp tools", nil
	}
	model.submit("/mcp tools")
	if len(model.entries) != 1 || model.entries[0].body != "mcp tools" {
		t.Fatalf("MCP command output was not shown: %#v", model.entries)
	}
}

func TestCtrlOTogglesToolDetails(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model.entries = []uiEntry{
		{kind: "tool", body: "Read · README.md", detail: "{\"path\":\"README.md\"}", toolState: "success"},
		{kind: "assistant", body: "结果"},
	}
	model.refreshViewport()
	if !strings.Contains(model.viewport.View(), "README.md") {
		t.Fatal("tool activity should be visible by default")
	}
	if strings.Contains(model.viewport.View(), `{"path":"README.md"}`) {
		t.Fatal("tool details should be collapsed by default")
	}
	model.handleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !model.expandToolOutput || !strings.Contains(model.viewport.View(), `{"path":"README.md"}`) {
		t.Fatalf("Ctrl+O did not expand tool details: expanded=%v view=%q", model.expandToolOutput, model.viewport.View())
	}
	model.handleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if model.expandToolOutput || !strings.Contains(model.viewport.View(), "README.md") || strings.Contains(model.viewport.View(), `{"path":"README.md"}`) {
		t.Fatalf("Ctrl+O did not collapse tool details: expanded=%v view=%q", model.expandToolOutput, model.viewport.View())
	}
}

func TestPromptHistoryNavigation(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.submit("/help")
	model.submit("/status")
	if len(model.history) != 2 {
		t.Fatalf("unexpected history: %#v", model.history)
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if model.input.Value() != "/status" {
		t.Fatalf("up did not restore latest prompt: %q", model.input.Value())
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if model.input.Value() != "/help" {
		t.Fatalf("second up did not restore older prompt: %q", model.input.Value())
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if model.input.Value() != "/status" {
		t.Fatalf("down did not move forward in history: %q", model.input.Value())
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if model.input.Value() != "" || model.historyIndex != -1 {
		t.Fatalf("down at newest did not clear draft: value=%q index=%d", model.input.Value(), model.historyIndex)
	}
}

func TestQuestionOptionNavigation(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	response := make(chan interactionResponse, 1)
	model.beginQuestion(questionRequestMsg{
		request:  tools.QuestionRequest{Question: "选择方案", Options: []string{"A", "B", "C"}},
		response: response,
	})
	model.handleInteractionKey(tea.KeyPressMsg{Code: tea.KeyDown})
	model.handleInteractionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	result := <-response
	if result.answer != "B" || model.pending != nil {
		t.Fatalf("question navigation selected wrong option: result=%#v pending=%#v", result, model.pending)
	}
}

func TestQuestionOptionsAllowFreeformAnswer(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	response := make(chan interactionResponse, 1)
	model.beginQuestion(questionRequestMsg{
		request:  tools.QuestionRequest{Question: "补充说明", Options: []string{"继续", "停止"}},
		response: response,
	})
	model.input.SetValue("需要人工确认")
	model.handleInteractionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	result := <-response
	if result.answer != "需要人工确认" || model.pending != nil {
		t.Fatalf("freeform question answer was not submitted: result=%#v pending=%#v", result, model.pending)
	}
}

func TestEscapeCancelsQuestionWithOptions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model := newAppModel(ctx, "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.busy = true
	model.cancel = cancel
	response := make(chan interactionResponse, 1)
	model.beginQuestion(questionRequestMsg{
		request:  tools.QuestionRequest{Question: "继续？", Options: []string{"是", "否"}},
		response: response,
	})
	_, command := model.handleInteractionKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command == nil || model.pending != nil || ctx.Err() == nil {
		t.Fatalf("question with options was not canceled: pending=%#v err=%v", model.pending, ctx.Err())
	}
}

func TestToolResultMatchesCallIDAndName(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.entries = []uiEntry{
		{kind: "tool", body: "custom tool · first", toolID: "one", toolNameValue: "custom_tool", toolState: "running"},
		{kind: "tool", body: "custom tool · second", toolID: "two", toolNameValue: "custom_tool", toolState: "running"},
	}
	model.markToolResult(messages.NewToolMessage("one", "custom_tool", "first result", false))
	if model.entries[0].toolState != "success" || model.entries[1].toolState != "running" {
		t.Fatalf("tool ID matching updated the wrong entry: %#v", model.entries)
	}
	model.markToolResult(messages.NewToolMessage("", "custom_tool", "second result", true))
	if model.entries[1].toolState != "error" || model.entries[1].detail != "second result" {
		t.Fatalf("tool name fallback did not update running entry: %#v", model.entries)
	}
}

func TestShortTerminalKeepsComposerAndFooter(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{Model: "model"}, nil, tools.NewRegistry(nil), tools.Context{})
	model.Update(tea.WindowSizeMsg{Width: 40, Height: 4})
	view := model.View().Content
	plainView := ansi.Strip(view)
	if !strings.Contains(plainView, "OpenAI") || !strings.Contains(plainView, "给 Banka") {
		t.Fatalf("short view lost composer/footer: %q", view)
	}
	if lipgloss.Height(view) > 4 {
		t.Fatalf("short view exceeds height: %d", lipgloss.Height(view))
	}
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 40 {
			t.Fatalf("short view line exceeds width: %d (%q)", width, line)
		}
	}
}

func TestViewportMouseCoordinatesFollowTranscriptLayout(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{}, nil, tools.NewRegistry(nil), tools.Context{})
	model.entries = []uiEntry{{kind: "assistant", body: "内容"}}
	model.resize()
	if position, ok := model.viewportPosition(1, 0); !ok || position.x != 0 || position.y != 0 {
		t.Fatalf("first transcript cell mapped incorrectly: position=%#v ok=%v", position, ok)
	}
	if _, ok := model.viewportPosition(0, 0); ok {
		t.Fatal("left outer padding should not be selectable")
	}
}
