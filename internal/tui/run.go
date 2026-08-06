// Package tui contains the terminal interaction mode.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/zhenxin-dev/banka-code/internal/agent"
	"github.com/zhenxin-dev/banka-code/internal/config"
	"github.com/zhenxin-dev/banka-code/internal/llm"
	"github.com/zhenxin-dev/banka-code/internal/messages"
	"github.com/zhenxin-dev/banka-code/internal/permissions"
	"github.com/zhenxin-dev/banka-code/internal/prompt"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

var tuiColors = struct {
	text, brand, shimmer, inactive, subtle, success, err, warning string
	user, assistant, tool, border, status, hint, divider          string
}{
	text: "#fff6f0", brand: "#ff8fb4", shimmer: "#ffd2df", inactive: "#f4e6df",
	subtle: "#d8bbb0", success: "#7fb069", err: "#ff9aa6", warning: "#ffc27a",
	user: "#ffd4a0", assistant: "#fff7f2", tool: "#e8c4a4", border: "#b89a96",
	status: "#ffe5cb", hint: "#d4bdb6", divider: "#c9a09c",
}

type uiEntry struct {
	kind string
	body string
}

// RuntimeDetails contains discovered capabilities shown by /status.
type RuntimeDetails struct {
	InstructionFiles int
	Skills           int
	MCP              []string
}

type animationTickMsg time.Time
type agentTextDeltaMsg string
type agentToolCallMsg messages.ToolCall
type agentToolResultMsg messages.Message
type agentFinishedMsg struct {
	result agent.RunResult
	err    error
}
type compactFinishedMsg struct {
	result agent.CompactResult
	err    error
}

type interactionResponse struct {
	decision tools.ApprovalDecision
	answer   string
}

type approvalRequestMsg struct {
	request  tools.ApprovalRequest
	response chan interactionResponse
}

type questionRequestMsg struct {
	request  tools.QuestionRequest
	response chan interactionResponse
}

type pendingInteraction struct {
	approval    *tools.ApprovalRequest
	question    *tools.QuestionRequest
	response    chan interactionResponse
	selection   int
	permissions bool
}

type approvalOption struct {
	label    string
	decision tools.ApprovalDecision
	status   string
}

type permissionModeOption struct {
	label string
	mode  permissions.Mode
}

func approvalOptions() []approvalOption {
	return []approvalOption{
		{label: "允许一次", decision: tools.ApprovalAllowOnce, status: "已允许本次受限操作"},
		{label: "始终允许此类操作（本次会话）", decision: tools.ApprovalAllowAlways, status: "本次会话将始终允许此类操作"},
		{label: "拒绝", decision: tools.ApprovalDeny, status: "已拒绝提权请求"},
	}
}

func permissionModeOptions() []permissionModeOption {
	return []permissionModeOption{
		{label: permissions.ModeDefault.Label(), mode: permissions.ModeDefault},
		{label: permissions.ModeFullAccess.Label(), mode: permissions.ModeFullAccess},
		{label: permissions.ModeYOLO.Label(), mode: permissions.ModeYOLO},
	}
}

type turnCheckpoint struct {
	entries    []uiEntry
	transcript []messages.Message
}

type tuiInteraction struct {
	events chan<- tea.Msg
}

func (i tuiInteraction) RequestApproval(ctx context.Context, request tools.ApprovalRequest) (tools.ApprovalDecision, error) {
	response := make(chan interactionResponse, 1)
	select {
	case i.events <- approvalRequestMsg{request: request, response: response}:
	case <-ctx.Done():
		return tools.ApprovalDeny, ctx.Err()
	}
	select {
	case result := <-response:
		return result.decision, nil
	case <-ctx.Done():
		return tools.ApprovalDeny, ctx.Err()
	}
}

func (i tuiInteraction) AskUser(ctx context.Context, request tools.QuestionRequest) (string, error) {
	response := make(chan interactionResponse, 1)
	select {
	case i.events <- questionRequestMsg{request: request, response: response}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case result := <-response:
		return result.answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type screenPosition struct {
	x int
	y int
}

type appModel struct {
	ctx          context.Context
	runtime      config.RuntimeConfig
	client       llm.Client
	registry     *tools.Registry
	toolContext  tools.Context
	systemPrompt string
	details      RuntimeDetails
	version      string

	input    textinput.Model
	viewport viewport.Model
	width    int
	height   int

	entries          []uiEntry
	transcript       []messages.Message
	busy             bool
	cancel           context.CancelFunc
	agentEvents      chan tea.Msg
	streamingEntry   int
	commandSelection int
	animationTick    int
	hitokoto         string
	selectionStart   *screenPosition
	selectionEnd     *screenPosition
	selectionMoved   bool
	pending          *pendingInteraction
	checkpoints      []turnCheckpoint
}

// Run starts the full-screen terminal mode.
func Run(ctx context.Context, input io.Reader, output io.Writer, version string, runtimeConfig config.RuntimeConfig, systemPrompt string, details RuntimeDetails, client llm.Client, registry *tools.Registry, toolContext tools.Context) error {
	model := newAppModel(ctx, version, runtimeConfig, client, registry, toolContext)
	model.systemPrompt = systemPrompt
	model.details = details
	program := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithFPS(30),
	)
	_, err := program.Run()
	if errors.Is(err, context.Canceled) || errors.Is(err, tea.ErrInterrupted) {
		return nil
	}
	return err
}

func newAppModel(ctx context.Context, version string, runtimeConfig config.RuntimeConfig, client llm.Client, registry *tools.Registry, toolContext tools.Context) *appModel {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = "给 Banka Code 发消息…"
	input.CharLimit = 0
	input.SetVirtualCursor(true)
	input.SetStyles(textinput.DefaultDarkStyles())
	view := viewport.New(viewport.WithWidth(80), viewport.WithHeight(16))
	view.MouseWheelEnabled = true
	view.SoftWrap = true
	view.FillHeight = true
	events := make(chan tea.Msg, 256)
	toolContext.Interaction = tuiInteraction{events: events}
	if toolContext.Permissions == nil {
		toolContext.Permissions = permissions.NewPolicy(runtimeConfig.PermissionMode)
	}
	return &appModel{
		ctx: ctx, runtime: runtimeConfig, client: client, registry: registry, toolContext: toolContext, version: version,
		systemPrompt: prompt.DefaultSystemPrompt,
		input:        input, viewport: view, width: 80, height: 24, streamingEntry: -1,
		agentEvents: events,
	}
}

func (m *appModel) Init() tea.Cmd {
	return tea.Batch(m.input.Focus(), animationTick(), fetchHitokoto)
}

func (m *appModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = max(20, message.Width)
		m.height = max(8, message.Height)
		m.resize()
		m.refreshViewport()
		return m, nil
	case animationTickMsg:
		m.animationTick++
		return m, animationTick()
	case hitokotoMsg:
		m.hitokoto = string(message)
		return m, nil
	case agentTextDeltaMsg:
		if m.streamingEntry >= 0 && m.streamingEntry < len(m.entries) {
			m.entries[m.streamingEntry].body += string(message)
			m.refreshViewport()
		}
		return m, waitForAgentEvent(m.agentEvents)
	case agentToolCallMsg:
		m.addToolCall(messages.ToolCall(message))
		m.refreshViewport()
		return m, waitForAgentEvent(m.agentEvents)
	case agentToolResultMsg:
		result := messages.Message(message)
		if result.IsError {
			m.entries = append(m.entries, uiEntry{kind: "error", body: result.ToolName + ": " + result.Content})
			m.refreshViewport()
		}
		return m, waitForAgentEvent(m.agentEvents)
	case approvalRequestMsg:
		m.beginApproval(message)
		return m, m.input.Focus()
	case questionRequestMsg:
		m.beginQuestion(message)
		return m, m.input.Focus()
	case agentFinishedMsg:
		m.finishTurn(message)
		return m, m.input.Focus()
	case compactFinishedMsg:
		m.finishCompaction(message)
		return m, m.input.Focus()
	case tea.MouseClickMsg:
		mouse := message.Mouse()
		if mouse.Button == tea.MouseLeft {
			if position, ok := m.viewportPosition(mouse.X, mouse.Y); ok {
				m.selectionStart = &position
				m.selectionEnd = &position
				m.selectionMoved = false
			}
		}
		return m, nil
	case tea.MouseMotionMsg:
		if m.selectionStart != nil {
			if position, ok := m.viewportPosition(message.Mouse().X, message.Mouse().Y); ok {
				m.selectionEnd = &position
				m.selectionMoved = position != *m.selectionStart
			}
		}
		return m, nil
	case tea.MouseReleaseMsg:
		if m.selectionStart == nil {
			return m, nil
		}
		if position, ok := m.viewportPosition(message.Mouse().X, message.Mouse().Y); ok {
			m.selectionEnd = &position
			m.selectionMoved = m.selectionMoved || position != *m.selectionStart
		}
		selected := ""
		if m.selectionMoved && m.selectionEnd != nil {
			selected = extractVisibleSelection(m.viewport.View(), *m.selectionStart, *m.selectionEnd)
		}
		m.selectionStart = nil
		m.selectionEnd = nil
		m.selectionMoved = false
		if selected != "" {
			return m, tea.SetClipboard(selected)
		}
		return m, nil
	case tea.MouseWheelMsg:
		var command tea.Cmd
		m.viewport, command = m.viewport.Update(message)
		return m, command
	case tea.PasteMsg:
		if !m.busy {
			var command tea.Cmd
			m.input, command = m.input.Update(message)
			m.commandSelection = 0
			m.resize()
			return m, command
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(message)
	}
	return m, nil
}

func (m *appModel) handleKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyName := key.String()
	if keyName == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if m.pending != nil {
		return m.handleInteractionKey(key)
	}
	if m.busy {
		if keyName == "esc" && m.cancel != nil {
			m.cancel()
		}
		return m, nil
	}

	suggestions := m.visibleCommandSuggestions()
	if len(suggestions) > 0 {
		switch keyName {
		case "up":
			m.commandSelection = moveCommandSelection(m.commandSelection, len(suggestions), -1)
			return m, nil
		case "down":
			m.commandSelection = moveCommandSelection(m.commandSelection, len(suggestions), 1)
			return m, nil
		case "tab":
			m.commandSelection = normalizeCommandSelection(m.commandSelection, len(suggestions))
			m.input.SetValue(suggestions[m.commandSelection].Command)
			m.input.CursorEnd()
			m.commandSelection = 0
			m.resize()
			return m, nil
		case "enter":
			m.commandSelection = normalizeCommandSelection(m.commandSelection, len(suggestions))
			return m.submit(suggestions[m.commandSelection].Command)
		}
	}

	switch keyName {
	case "enter":
		return m.submit(m.input.Value())
	case "pgup":
		m.viewport.PageUp()
		return m, nil
	case "pgdown":
		m.viewport.PageDown()
		return m, nil
	}

	var command tea.Cmd
	m.input, command = m.input.Update(key)
	m.commandSelection = 0
	m.resize()
	return m, command
}

func (m *appModel) handleInteractionKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.pending.approval != nil || m.pending.permissions {
		count := len(approvalOptions())
		if m.pending.permissions {
			count = len(permissionModeOptions())
		}
		switch key.String() {
		case "up":
			m.pending.selection = moveCommandSelection(m.pending.selection, count, -1)
			m.resize()
			return m, nil
		case "down":
			m.pending.selection = moveCommandSelection(m.pending.selection, count, 1)
			m.resize()
			return m, nil
		case "enter":
			if m.pending.permissions {
				return m.selectPermissionMode()
			}
			return m.selectApproval()
		case "esc":
			if m.pending.permissions {
				m.pending = nil
				m.input.Placeholder = "给 Banka Code 发消息…"
				m.resize()
				return m, m.input.Focus()
			}
			return m.resolveInteraction(interactionResponse{decision: tools.ApprovalDeny}, "已拒绝提权请求")
		}
		return m, nil
	}
	switch key.String() {
	case "esc":
		m.pending = nil
		m.input.Reset()
		m.input.Placeholder = "等待中…"
		if m.cancel != nil {
			m.cancel()
		}
		return m, waitForAgentEvent(m.agentEvents)
	case "enter":
		return m.submitInteraction(m.input.Value())
	}
	var command tea.Cmd
	m.input, command = m.input.Update(key)
	m.resize()
	return m, command
}

func (m *appModel) beginApproval(message approvalRequestMsg) {
	request := message.request
	m.pending = &pendingInteraction{approval: &request, response: message.response, selection: 0}
	m.input.Reset()
	m.input.Placeholder = "使用方向键选择…"
	m.entries = append(m.entries, uiEntry{kind: "approval", body: fmt.Sprintf(
		"请求执行受限操作\n\n操作：`%s`\n\n理由：%s", request.Command, request.Justification,
	)})
	m.resize()
	m.refreshViewport()
}

func (m *appModel) beginPermissionModeSelection() {
	selection := 0
	current := m.toolContext.PermissionMode()
	for index, option := range permissionModeOptions() {
		if option.mode == current {
			selection = index
			break
		}
	}
	m.pending = &pendingInteraction{permissions: true, selection: selection}
	m.input.Reset()
	m.input.Placeholder = "使用方向键选择…"
	m.resize()
}

func (m *appModel) beginQuestion(message questionRequestMsg) {
	request := message.request
	m.pending = &pendingInteraction{question: &request, response: message.response}
	m.input.Reset()
	m.input.Placeholder = "输入回答…"
	body := request.Question
	for index, option := range request.Options {
		body += fmt.Sprintf("\n\n%d. %s", index+1, option)
	}
	m.entries = append(m.entries, uiEntry{kind: "question", body: body})
	m.refreshViewport()
}

func (m *appModel) submitInteraction(value string) (tea.Model, tea.Cmd) {
	answer := strings.TrimSpace(value)
	if answer == "" {
		return m, nil
	}
	if index, err := strconv.Atoi(answer); err == nil && index >= 1 && index <= len(m.pending.question.Options) {
		answer = m.pending.question.Options[index-1]
	}
	m.entries = append(m.entries, uiEntry{kind: "user", body: answer})
	return m.resolveInteraction(interactionResponse{answer: answer}, "")
}

func (m *appModel) selectApproval() (tea.Model, tea.Cmd) {
	options := approvalOptions()
	selection := normalizeCommandSelection(m.pending.selection, len(options))
	option := options[selection]
	return m.resolveInteraction(interactionResponse{decision: option.decision}, option.status)
}

func (m *appModel) selectPermissionMode() (tea.Model, tea.Cmd) {
	options := permissionModeOptions()
	selection := normalizeCommandSelection(m.pending.selection, len(options))
	mode := options[selection].mode
	if m.toolContext.Permissions == nil {
		m.toolContext.Permissions = permissions.NewPolicy(mode)
	} else {
		m.toolContext.Permissions.SetMode(mode)
	}
	m.runtime.PermissionMode = mode
	m.pending = nil
	m.input.Reset()
	m.input.Placeholder = "给 Banka Code 发消息…"
	m.entries = append(m.entries, uiEntry{kind: "status", body: "权限模式已切换为" + mode.Label()})
	m.resize()
	m.refreshViewport()
	return m, m.input.Focus()
}

func (m *appModel) resolveInteraction(response interactionResponse, status string) (tea.Model, tea.Cmd) {
	pending := m.pending
	m.pending = nil
	m.input.Reset()
	m.input.Placeholder = "等待中…"
	if status != "" {
		m.entries = append(m.entries, uiEntry{kind: "status", body: status})
	}
	m.resize()
	m.refreshViewport()
	pending.response <- response
	return m, waitForAgentEvent(m.agentEvents)
}

func (m *appModel) submit(value string) (tea.Model, tea.Cmd) {
	userPrompt := strings.TrimSpace(value)
	if userPrompt == "" {
		return m, nil
	}
	m.input.Reset()
	m.commandSelection = 0
	m.resize()

	if command, ok := parseBuiltinCommand(userPrompt); ok {
		switch command.Name {
		case "exit", "quit":
			return m, tea.Quit
		case "clear":
			m.entries = nil
			m.transcript = nil
			m.checkpoints = nil
			m.streamingEntry = -1
			m.refreshViewport()
			return m, nil
		case "undo":
			m.undoLastTurn()
			return m, nil
		case "compact":
			return m.startCompaction()
		case "permissions":
			m.beginPermissionModeSelection()
			return m, nil
		case "help":
			m.entries = append(m.entries, uiEntry{kind: "assistant", body: buildBuiltinHelpBody()})
			m.refreshViewport()
			return m, nil
		case "status":
			body := fmt.Sprintf("## 当前状态\n\n- Provider：%s\n- Model：%s\n- 权限模式：%s\n- 已注册工具：%d\n- AGENTS 指令文件：%d\n- Skills：%d\n- 当前屏幕消息数：%d\n- 当前会话消息数：%d\n- 可回滚轮数：%d",
				providerLabel(m.runtime.Provider), m.runtime.Model, m.toolContext.PermissionMode().Label(), len(m.registry.List()), m.details.InstructionFiles,
				m.details.Skills, len(m.entries), len(m.transcript), len(m.checkpoints))
			if len(m.details.MCP) == 0 {
				body += "\n- MCP：未配置"
			} else {
				body += "\n- MCP：" + strings.Join(m.details.MCP, "；")
			}
			m.entries = append(m.entries, uiEntry{kind: "assistant", body: body})
			m.refreshViewport()
			return m, nil
		}
	}

	m.saveCheckpoint()
	m.busy = true
	m.input.Placeholder = "等待中…"
	m.entries = append(m.entries, uiEntry{kind: "user", body: userPrompt})
	m.entries = append(m.entries, uiEntry{kind: "assistant"})
	m.streamingEntry = len(m.entries) - 1
	m.refreshViewport()

	runContext, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	previous := append([]messages.Message(nil), m.transcript...)
	return m, tea.Batch(
		runAgent(runContext, m.agentEvents, m.systemPrompt, m.client, m.registry, m.toolContext, previous, userPrompt),
		waitForAgentEvent(m.agentEvents),
	)
}

func (m *appModel) startCompaction() (tea.Model, tea.Cmd) {
	m.saveCheckpoint()
	m.busy = true
	m.input.Placeholder = "正在压缩上下文…"
	m.entries = append(m.entries, uiEntry{kind: "status", body: "正在压缩较早的会话上下文…"})
	m.refreshViewport()
	runContext, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	transcript := append([]messages.Message(nil), m.transcript...)
	return m, func() tea.Msg {
		result, err := agent.Compact(runContext, m.client, transcript, 2)
		return compactFinishedMsg{result: result, err: err}
	}
}

func (m *appModel) finishCompaction(message compactFinishedMsg) {
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = nil
	m.busy = false
	m.input.Placeholder = "给 Banka Code 发消息…"
	if message.err != nil {
		if len(m.checkpoints) > 0 {
			m.checkpoints = m.checkpoints[:len(m.checkpoints)-1]
		}
		if !errors.Is(message.err, context.Canceled) {
			m.entries = append(m.entries, uiEntry{kind: "error", body: message.err.Error()})
		}
		m.refreshViewport()
		return
	}
	m.transcript = message.result.Transcript
	m.entries = append(m.entries, uiEntry{kind: "status", body: fmt.Sprintf(
		"已将 %d 条较早消息压缩为摘要，最近两轮保留原文", message.result.CompactedMessages,
	)})
	m.refreshViewport()
}

func (m *appModel) saveCheckpoint() {
	m.checkpoints = append(m.checkpoints, turnCheckpoint{
		entries:    append([]uiEntry(nil), m.entries...),
		transcript: append([]messages.Message(nil), m.transcript...),
	})
}

func (m *appModel) undoLastTurn() {
	if len(m.checkpoints) == 0 {
		m.entries = append(m.entries, uiEntry{kind: "status", body: "没有可回滚的会话"})
		m.refreshViewport()
		return
	}
	checkpoint := m.checkpoints[len(m.checkpoints)-1]
	m.checkpoints = m.checkpoints[:len(m.checkpoints)-1]
	m.entries = append([]uiEntry(nil), checkpoint.entries...)
	m.transcript = append([]messages.Message(nil), checkpoint.transcript...)
	m.streamingEntry = -1
	m.entries = append(m.entries, uiEntry{kind: "status", body: "已回滚上一轮会话上下文；工作区文件未更改"})
	m.refreshViewport()
}

func runAgent(ctx context.Context, events chan<- tea.Msg, systemPrompt string, client llm.Client, registry *tools.Registry, toolContext tools.Context, previous []messages.Message, userPrompt string) tea.Cmd {
	return func() tea.Msg {
		result, err := agent.Run(ctx, agent.RunOptions{
			SystemPrompt:      systemPrompt,
			InitialUserPrompt: userPrompt,
			PreviousMessages:  previous,
			Model:             client,
			ToolRegistry:      registry,
			ToolContext:       toolContext,
			OnTextDelta: func(delta string) {
				events <- agentTextDeltaMsg(delta)
			},
			OnToolCall: func(call messages.ToolCall) {
				events <- agentToolCallMsg(call)
			},
			OnToolResult: func(result messages.Message) {
				events <- agentToolResultMsg(result)
			},
		})
		events <- agentFinishedMsg{result: result, err: err}
		return nil
	}
}

func waitForAgentEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}

func (m *appModel) addToolCall(call messages.ToolCall) {
	toolEntry := uiEntry{kind: "tool", body: formatToolCall(call)}
	if m.streamingEntry >= 0 && m.streamingEntry < len(m.entries) && m.entries[m.streamingEntry].body == "" {
		m.entries[m.streamingEntry] = toolEntry
	} else {
		m.entries = append(m.entries, toolEntry)
	}
	m.entries = append(m.entries, uiEntry{kind: "assistant"})
	m.streamingEntry = len(m.entries) - 1
}

func (m *appModel) finishTurn(message agentFinishedMsg) {
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = nil
	m.pending = nil
	m.busy = false
	m.input.Placeholder = "给 Banka Code 发消息…"

	if message.err == nil {
		if m.streamingEntry >= 0 && m.streamingEntry < len(m.entries) && m.entries[m.streamingEntry].body == "" && message.result.FinalText != "" {
			m.entries[m.streamingEntry].body = message.result.FinalText
		}
		m.transcript = message.result.Transcript
		m.removeEmptyStreamingEntry()
		m.entries = append(m.entries, uiEntry{kind: "status", body: fmt.Sprintf("✓ %d 轮", message.result.Iterations)})
	} else {
		m.removeEmptyStreamingEntry()
		if !errors.Is(message.err, context.Canceled) {
			m.entries = append(m.entries, uiEntry{kind: "error", body: message.err.Error()})
		}
	}
	m.streamingEntry = -1
	m.refreshViewport()
}

func (m *appModel) removeEmptyStreamingEntry() {
	if m.streamingEntry >= 0 && m.streamingEntry < len(m.entries) && m.entries[m.streamingEntry].body == "" {
		m.entries = append(m.entries[:m.streamingEntry], m.entries[m.streamingEntry+1:]...)
	}
}

func (m *appModel) visibleCommandSuggestions() []builtinCommand {
	commands := findBuiltinCommands(m.input.Value())
	if len(commands) > 6 {
		return commands[:6]
	}
	return commands
}

func (m *appModel) viewportPosition(x int, y int) (screenPosition, bool) {
	const viewportTop = 3
	const viewportLeft = 1
	position := screenPosition{x: x - viewportLeft, y: y - viewportTop}
	return position, position.x >= 0 && position.x < m.viewport.Width() && position.y >= 0 && position.y < m.viewport.Height()
}

func extractVisibleSelection(view string, start screenPosition, end screenPosition) string {
	if end.y < start.y || (end.y == start.y && end.x < start.x) {
		start, end = end, start
	}
	lines := strings.Split(view, "\n")
	if start.y < 0 || start.y >= len(lines) || end.y < 0 || end.y >= len(lines) {
		return ""
	}
	var selected []string
	for lineIndex := start.y; lineIndex <= end.y; lineIndex++ {
		line := lines[lineIndex]
		left := 0
		right := ansi.StringWidth(line)
		if lineIndex == start.y {
			left = min(max(0, start.x), right)
		}
		if lineIndex == end.y {
			right = min(max(0, end.x+1), right)
		}
		if right < left {
			right = left
		}
		selected = append(selected, strings.TrimRight(ansi.Strip(ansi.Cut(line, left, right)), " "))
	}
	return strings.TrimSpace(strings.Join(selected, "\n"))
}

func (m *appModel) resize() {
	contentWidth := max(16, m.width-4)
	m.input.SetWidth(max(8, contentWidth-4))
	panelHeight := 0
	if panel := m.selectionPanelView(contentWidth); panel != "" {
		panelHeight = lipgloss.Height(panel)
	} else if commands := m.visibleCommandSuggestions(); !m.busy && strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/") {
		panelHeight = max(1, len(commands)) + 2
	}
	viewportHeight := max(1, m.height-8-panelHeight)
	m.viewport.SetWidth(contentWidth)
	m.viewport.SetHeight(viewportHeight)
}

func (m *appModel) refreshViewport() {
	wasAtBottom := m.viewport.AtBottom()
	var blocks []string
	for _, entry := range m.entries {
		if rendered := m.renderEntry(entry); rendered != "" {
			blocks = append(blocks, rendered)
		}
	}
	m.viewport.SetContent(strings.Join(blocks, "\n"))
	if wasAtBottom || m.busy {
		m.viewport.GotoBottom()
	}
}

func (m *appModel) renderEntry(entry uiEntry) string {
	switch entry.kind {
	case "user":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.user)).PaddingLeft(2).Render("◈ " + entry.body)
	case "assistant":
		if entry.body == "" {
			return ""
		}
		body := renderMarkdown(entry.body, max(20, m.viewport.Width()-6))
		return lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.assistant)).PaddingLeft(2).Render("❀ " + body)
	case "tool":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.tool)).Faint(true).PaddingLeft(4).Render("⊰ " + entry.body)
	case "approval":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.warning)).PaddingLeft(3).Render("⚠ " + renderMarkdown(entry.body, max(20, m.viewport.Width()-6)))
	case "question":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.assistant)).PaddingLeft(2).Render("? " + renderMarkdown(entry.body, max(20, m.viewport.Width()-6)))
	case "error":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.err)).PaddingLeft(3).Render("⚠ " + entry.body)
	case "status":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.status)).Faint(true).PaddingLeft(3).Render("◌ " + entry.body)
	default:
		return entry.body
	}
}

func renderMarkdown(content string, width int) string {
	renderer, err := glamour.NewTermRenderer(glamour.WithStylePath("pink"), glamour.WithWordWrap(width))
	if err != nil {
		return content
	}
	rendered, err := renderer.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimSpace(rendered)
}

func (m *appModel) View() tea.View {
	contentWidth := max(16, m.width-4)
	header := lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center).Render(m.logoView())
	divider := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.divider)).Render(horizontalRule(contentWidth, "─"))
	parts := []string{header, divider, m.viewport.View()}
	if panel := m.selectionPanelView(contentWidth); panel != "" {
		parts = append(parts, panel)
	} else if panel := m.commandPanelView(contentWidth); panel != "" {
		parts = append(parts, panel)
	}
	parts = append(parts, m.inputView(contentWidth), m.statusView(contentWidth))
	content := lipgloss.NewStyle().Width(m.width).Height(m.height).PaddingLeft(1).PaddingRight(1).Render(strings.Join(parts, "\n"))
	view := tea.NewView(content)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "Banka Code"
	return view
}

func (m *appModel) logoView() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(tuiColors.text)).Render("Banka Code")
	version := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.inactive)).Render(" v" + m.version + "  ")
	var greeting strings.Builder
	for index, character := range []rune("Ciallo～(∠・ω< )⌒☆") {
		color := logoCharacterColor(index, m.animationTick)
		if character == '☆' && m.animationTick%10 < 2 {
			color = "#ffffff"
		}
		greeting.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(string(character)))
	}
	quote := ""
	if m.hitokoto != "" {
		quote = lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.inactive)).Render("「" + m.hitokoto + "」")
	}
	return title + version + greeting.String() + "\n" + quote
}

func (m *appModel) commandPanelView(width int) string {
	if m.pending != nil || m.busy || !strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/") {
		return ""
	}
	commands := m.visibleCommandSuggestions()
	if len(commands) == 0 {
		return lipgloss.NewStyle().Width(width - 2).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(tuiColors.border)).PaddingLeft(1).Render("未找到命令，输入 /help 查看可用命令")
	}
	selected := normalizeCommandSelection(m.commandSelection, len(commands))
	lines := make([]string, 0, len(commands))
	for index, command := range commands {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.hint))
		if index == selected {
			marker = "› "
			style = style.Foreground(lipgloss.Color(tuiColors.shimmer)).Bold(true)
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s%-10s %s", marker, command.Command, command.Description)))
	}
	return lipgloss.NewStyle().Width(width - 2).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(tuiColors.border)).PaddingLeft(1).Render(strings.Join(lines, "\n"))
}

func (m *appModel) selectionPanelView(width int) string {
	if m.pending == nil || (m.pending.approval == nil && !m.pending.permissions) {
		return ""
	}
	labels := make([]string, 0, 3)
	if m.pending.permissions {
		for _, option := range permissionModeOptions() {
			labels = append(labels, option.label)
		}
	} else {
		for _, option := range approvalOptions() {
			labels = append(labels, option.label)
		}
	}
	selected := normalizeCommandSelection(m.pending.selection, len(labels))
	lines := make([]string, 0, len(labels))
	for index, label := range labels {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.hint)).MaxWidth(max(4, width-6))
		if index == selected {
			marker = "› "
			style = style.Foreground(lipgloss.Color(tuiColors.shimmer)).Bold(true)
		}
		lines = append(lines, style.Render(marker+label))
	}
	return lipgloss.NewStyle().Width(width - 2).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(tuiColors.warning)).PaddingLeft(1).Render(strings.Join(lines, "\n"))
}

func (m *appModel) inputView(width int) string {
	marker := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.shimmer)).Bold(true).Render("◈ ")
	if m.busy {
		marker = lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.status)).Render("◇ ")
	}
	content := marker + m.input.View()
	if m.pending != nil && (m.pending.approval != nil || m.pending.permissions) {
		content = marker + "↑↓ · Enter · Esc"
	}
	return lipgloss.NewStyle().Width(width - 2).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(tuiColors.border)).PaddingLeft(1).Render(content)
}

func (m *appModel) statusView(width int) string {
	left := " "
	if m.busy {
		left = statusMarquee(m.animationTick) + " " + lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.hint)).Render("按 ESC 中断")
	}
	right := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.hint)).Faint(true).Render(providerLabel(m.runtime.Provider) + " · " + m.runtime.Model + " · " + m.toolContext.PermissionMode().Label())
	space := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", space) + right
}

func providerLabel(provider config.ProviderKind) string {
	if provider == config.ProviderAnthropic {
		return "Anthropic"
	}
	return "OpenAI"
}

func animationTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(now time.Time) tea.Msg { return animationTickMsg(now) })
}

func statusMarquee(tick int) string {
	const width = 7
	period := width*2 - 2
	raw := tick % period
	head := raw
	if raw > width-1 {
		head = period - raw
	}
	trail := []struct {
		glyph string
		color string
	}{
		{"●", "#ffd27a"}, {"◉", "#ffb347"}, {"◎", "#f2966b"},
		{"○", "#e07060"}, {"◌", "#a04840"}, {"·", "#602a24"},
	}
	var result strings.Builder
	for index := 0; index < width; index++ {
		distance := index - head
		if distance < 0 {
			distance = -distance
		}
		if distance < len(trail) {
			result.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(trail[distance].color)).Render(trail[distance].glyph))
		} else {
			result.WriteByte(' ')
		}
	}
	return result.String()
}

var logoPalette = []string{"#ff94b8", "#ff7cab", "#ff6699", "#ff8a66", "#ffb347", "#ff8a66", "#ff6699", "#ff7cab"}

func logoCharacterColor(index int, tick int) string {
	const waveSpan = 4.0
	cycle := float64(len(logoPalette)) + waveSpan*2
	position := math.Mod(float64(tick)*0.4-float64(index)*0.7, cycle)
	if position < 0 {
		position += cycle
	}
	if position < waveSpan {
		return interpolateHex("#b98e84", logoPalette[0], position/waveSpan)
	}
	if position < waveSpan+float64(len(logoPalette)) {
		rawIndex := position - waveSpan
		low := int(math.Floor(rawIndex))
		high := (low + 1) % len(logoPalette)
		return interpolateHex(logoPalette[low], logoPalette[high], rawIndex-float64(low))
	}
	fade := 1 - (position-waveSpan-float64(len(logoPalette)))/waveSpan
	return interpolateHex("#b98e84", logoPalette[0], max(0.0, min(1.0, fade)))
}

func interpolateHex(first string, second string, amount float64) string {
	parse := func(value string) (int, int, int) {
		number, _ := strconv.ParseInt(strings.TrimPrefix(value, "#"), 16, 64)
		return int(number >> 16 & 0xff), int(number >> 8 & 0xff), int(number & 0xff)
	}
	firstRed, firstGreen, firstBlue := parse(first)
	secondRed, secondGreen, secondBlue := parse(second)
	red := int(math.Round(float64(firstRed) + float64(secondRed-firstRed)*amount))
	green := int(math.Round(float64(firstGreen) + float64(secondGreen-firstGreen)*amount))
	blue := int(math.Round(float64(firstBlue) + float64(secondBlue-firstBlue)*amount))
	return fmt.Sprintf("#%02x%02x%02x", red, green, blue)
}
